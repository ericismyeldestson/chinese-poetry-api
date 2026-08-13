package processor

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/vbauerster/mpb/v8"
	"github.com/vbauerster/mpb/v8/decor"
	"go.uber.org/zap"
	"gorm.io/datatypes"

	"github.com/ericismyeldestson/chinese-poetry-api/internal/classifier"
	"github.com/ericismyeldestson/chinese-poetry-api/internal/database"
	"github.com/ericismyeldestson/chinese-poetry-api/internal/loader"
	"github.com/ericismyeldestson/chinese-poetry-api/internal/logger"
)

const (
	MaxErrorsToDisplay = 100 // 最多展示的错误数量
	MaxErrorsToCollect = 100 // 最多收集的错误数量
	SampleErrorCount   = 5   // 出错时打印的错误样本数量
)

// getOptimalConfig 根据机器 CPU 核数返回各类缓冲区与批量大小的推荐值。
// 核数越多配置越激进：2 核（CI 环境）保守，4-8 核折中，10 核以上放开。
func getOptimalConfig() (workBuffer, resultBuffer, errorBuffer, defaultBatch, minBatch, maxBatch int) {
	cpuCount := runtime.NumCPU()

	switch {
	case cpuCount <= 2:
		// GitHub Actions 等低配 CI
		return 50, 1000, 50, 200, 50, 300

	case cpuCount <= 4:
		// 入门级机器
		return 75, 2000, 75, 300, 100, 500

	case cpuCount <= 8:
		// 中端机器
		return 100, 3000, 100, 400, 150, 700

	default:
		// 高端机器
		return 500, 10000, 500, 1000, 500, 2000
	}
}

// Processor 负责并发处理诗词数据。
type Processor struct {
	repo                 database.RepositoryInterface
	workers              int
	convertToTraditional bool
	batchSize            int // 写入数据库时的批量大小
}

// PartitionSources classifies source-level rejections exactly once, before the
// Hans/Hant branches diverge. Rejected records remain represented in the global
// source_rejections ledger; only accepted records proceed to product generation.
func PartitionSources(poems []loader.PoemWithMeta) ([]loader.PoemWithMeta, []database.SourceRejection, error) {
	accepted := make([]loader.PoemWithMeta, 0, len(poems))
	rejections := make([]database.SourceRejection, 0)
	for i := range poems {
		poem := &poems[i]
		if poem.RejectionReason == "" {
			paragraphs := classifier.NormalizeAndSplitParagraphs(poem.Paragraphs)
			switch {
			case containsUnicodeReplacementCharacter(*poem):
				poem.RejectionStage = "normalization"
				poem.RejectionReason = "unicode_replacement_character"
			case len(paragraphs) == 0:
				poem.RejectionStage = "normalization"
				poem.RejectionReason = "empty_after_normalization"
			case classifier.IsPlaceholderContent(paragraphs):
				poem.RejectionStage = "normalization"
				poem.RejectionReason = "placeholder_content"
			}
		}

		if poem.RejectionReason == "" {
			accepted = append(accepted, *poem)
			continue
		}
		if poem.RejectionStage == "" {
			return nil, nil, fmt.Errorf("source %s#%d has a rejection reason without a stage", poem.SourcePath, poem.SourceRecordIndex)
		}
		rejection, err := database.NewSourceRejection(
			poem.SourceID, poem.DatasetKey, poem.SourcePath, poem.SourceRecordIndex,
			poem.RejectionStage, poem.RejectionReason,
		)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid rejection for source %s#%d: %w", poem.SourcePath, poem.SourceRecordIndex, err)
		}
		rejections = append(rejections, rejection)
	}
	return accepted, rejections, nil
}

func containsUnicodeReplacementCharacter(poem loader.PoemWithMeta) bool {
	if strings.ContainsRune(poem.Title, '\uFFFD') || strings.ContainsRune(poem.Author, '\uFFFD') ||
		strings.ContainsRune(poem.Chapter, '\uFFFD') || strings.ContainsRune(poem.Rhythmic, '\uFFFD') {
		return true
	}
	for _, paragraph := range poem.Paragraphs {
		if strings.ContainsRune(paragraph, '\uFFFD') {
			return true
		}
	}
	return false
}

// NewProcessor 创建带缓存能力的处理器，workers <= 0 时按 CPU 核数取值。
func NewProcessor(repo *database.Repository, workers int, convertToTraditional bool) *Processor {
	if workers <= 0 {
		workers = runtime.NumCPU()
	}

	_, _, _, defaultBatch, _, _ := getOptimalConfig()

	// 包一层缓存，避免重复查询朝代/作者
	cachedRepo := database.NewCachedRepository(repo)

	return &Processor{
		repo:                 cachedRepo,
		workers:              workers,
		convertToTraditional: convertToTraditional,
		batchSize:            defaultBatch,
	}
}

// SetBatchSize 设置写入数据库时的批量大小。
func (p *Processor) SetBatchSize(size int) {
	if size > 0 {
		p.batchSize = size
	}
}

// prewarmCache 预先把朝代、作者写入缓存。
// 若不预热，所有 worker 会在冷缓存下同时读写数据库，
// 在 SQLite 单写者模型下会造成锁竞争，表现为疑似死锁。
func (p *Processor) prewarmCache(poems []loader.PoemWithMeta) error {
	// 先收集去重后的朝代（数量很少，约 20 个）
	dynastySet := make(map[string]struct{})
	for _, poem := range poems {
		if poem.Dynasty != "" {
			dynasty := poem.Dynasty
			converted, err := p.convertText(dynasty, p.convertToTraditional)
			if err != nil {
				continue // 出错则跳过，留到正式处理阶段再报
			}
			dynastySet[converted] = struct{}{}
		}
	}

	// 串行预热朝代缓存，无并发风险
	dynasties := make([]string, 0, len(dynastySet))
	for dynasty := range dynastySet {
		dynasties = append(dynasties, dynasty)
	}
	sort.Strings(dynasties)
	for _, dynasty := range dynasties {
		if _, err := p.repo.GetOrCreateDynasty(dynasty); err != nil {
			return fmt.Errorf("failed to pre-warm dynasty cache for %q: %w", dynasty, err)
		}
	}

	// 收集去重后的作者。身份键必须同时包含朝代，否则会把
	// 数百个跨朝代同名人合并。
	type authorWarmKey struct {
		canonicalName    string
		canonicalDynasty string
		displayName      string
		displayDynasty   string
		canonicalID      string
	}
	authorSet := make(map[authorWarmKey]struct{})
	for _, poem := range poems {
		author := classifier.NormalizeText(poem.Author)
		if author == "" {
			author = "佚名"
		}
		canonicalName, err := canonicalSimplifiedText(author)
		if err != nil {
			continue
		}
		canonicalDynasty, err := canonicalSimplifiedText(poem.Dynasty)
		if err != nil || canonicalDynasty == "" {
			continue
		}
		canonicalID, err := database.NewCanonicalAuthorID(canonicalDynasty, canonicalName)
		if err != nil {
			continue
		}
		displayName, displayDynasty := canonicalName, canonicalDynasty
		if p.convertToTraditional {
			displayName, _ = classifier.ToTraditional(canonicalName)
			displayDynasty, _ = classifier.ToTraditional(canonicalDynasty)
		}
		authorSet[authorWarmKey{
			canonicalName: canonicalName, canonicalDynasty: canonicalDynasty,
			displayName: displayName, displayDynasty: displayDynasty,
			canonicalID: canonicalID,
		}] = struct{}{}
	}

	// 预热作者缓存
	authors := make([]authorWarmKey, 0, len(authorSet))
	for author := range authorSet {
		authors = append(authors, author)
	}
	sort.Slice(authors, func(i, j int) bool {
		if authors[i].canonicalDynasty != authors[j].canonicalDynasty {
			return authors[i].canonicalDynasty < authors[j].canonicalDynasty
		}
		return authors[i].canonicalName < authors[j].canonicalName
	})
	for _, author := range authors {
		dynastyID, err := p.repo.GetOrCreateDynasty(author.displayDynasty)
		if err != nil {
			continue // 留到正式处理阶段再报
		}
		if _, err := p.repo.GetOrCreateCanonicalAuthor(author.canonicalID, author.displayName, dynastyID); err != nil {
			continue // 预热失败不影响流程，正式处理时会重试
		}
	}

	logger.Info("Cache pre-warmed",
		zap.Int("dynasties", len(dynastySet)),
		zap.Int("authors", len(authorSet)),
	)

	return nil
}

// Process 以多 worker 并发处理全部诗词，并批量写入数据库。
func (p *Processor) Process(poems []loader.PoemWithMeta) error {
	total := len(poems)
	logger.Info("Processing poems",
		zap.Int("total", total),
		zap.Int("workers", p.workers),
		zap.Int("batch_size", p.batchSize),
	)

	// 启动 worker 前先预热缓存，避免冷缓存下集中冲击数据库
	if err := p.prewarmCache(poems); err != nil {
		return fmt.Errorf("failed to pre-warm cache: %w", err)
	}

	// 进度条容器
	progress := mpb.New(
		mpb.WithWidth(60),
		mpb.WithRefreshRate(100*time.Millisecond),
	)

	bar := progress.AddBar(int64(total),
		mpb.PrependDecorators(
			decor.Name("Processing: ", decor.WC{W: 12, C: decor.DindentRight}),
			decor.CountersNoUnit("%d / %d", decor.WCSyncWidth),
		),
		mpb.AppendDecorators(
			decor.Percentage(decor.WC{W: 5}),
			decor.Name(" | "),
			decor.AverageETA(decor.ET_STYLE_GO, decor.WC{W: 6}),
			decor.Name(" | "),
			decor.AverageSpeed(0, "%.0f poems/s", decor.WC{W: 12}),
		),
	)

	// 任务分发用的 channel，缓冲区大小随机器配置自适应
	workBuffer, resultBuffer, errorBuffer, _, _, _ := getOptimalConfig()

	workCh := make(chan PoemWork, workBuffer)
	resultCh := make(chan *database.Poem, resultBuffer)
	errorCh := make(chan error, errorBuffer)
	var wg sync.WaitGroup

	// 进度计数
	var processed atomic.Int64
	var errorCount atomic.Int64

	// 启动 worker 处理诗词（CPU 密集型）
	for i := range p.workers {
		wg.Go(func() {
			for work := range workCh {
				poem, err := p.processPoem(work)
				if err != nil {
					errorCount.Add(1)
					// 非阻塞记录错误
					select {
					case errorCh <- fmt.Errorf("worker %d: %s - %w", i, work.Title, err):
					default:
						// 通道已满则丢弃，避免阻塞
					}
					processed.Add(1)
					bar.Increment()
					continue
				}

				// 跳过 nil（如归一化后正文为空的条目）
				if poem == nil {
					processed.Add(1)
					bar.Increment()
					continue
				}

				resultCh <- poem
				processed.Add(1)
				bar.Increment()
			}
		})
	}

	// 启动批量写入 goroutine
	insertDone := make(chan error, 1)
	go func() {
		insertDone <- p.batchInserter(resultCh, &errorCount)
	}()

	// 分发任务
	go func() {
		for i, poem := range poems {
			workCh <- PoemWork{
				PoemWithMeta:  poem,
				SourceOrdinal: int64(i + 1),
			}
		}
		close(workCh)
	}()

	wg.Wait()

	// 写入阶段开始前，先让处理进度条收尾
	bar.SetTotal(int64(total), true) // 标记为已完成
	progress.Wait()                  // 等待进度条渲染结束

	close(resultCh) // 通知批量写入协程收尾

	if err := <-insertDone; err != nil {
		return fmt.Errorf("batch insertion failed: %w", err)
	}

	close(errorCh)

	// 收集错误（此时通道已关闭，不会阻塞）
	var errors []error
	for err := range errorCh {
		errors = append(errors, err)
		if len(errors) >= MaxErrorsToCollect {
			break
		}
	}

	// 输出汇总结果
	successCount := processed.Load()
	failCount := errorCount.Load()

	if failCount > 0 {
		logger.Warn("Processing completed with errors",
			zap.Int64("success", successCount-failCount),
			zap.Int64("failed", failCount),
			zap.Int("total", total),
		)
		if len(errors) > 0 {
			for i := range min(len(errors), SampleErrorCount) {
				logger.Debug("Sample error", zap.Int("index", i+1), zap.Error(errors[i]))
			}
		}
		return fmt.Errorf("processing completed with %d errors", failCount)
	}

	logger.Info("Processing completed successfully", zap.Int("total", total))
	return nil
}

// batchInserter 汇总处理完的诗词，用大事务批量写库。
// 把大量 INSERT 合并到少数几个事务里，可显著降低 fsync 开销。
func (p *Processor) batchInserter(resultCh <-chan *database.Poem, processingErrors *atomic.Int64) error {
	// 先收齐所有已处理的诗词，顺带过滤 nil 作为兜底
	allPoems := make([]*database.Poem, 0, cap(resultCh))

	for poem := range resultCh {
		if poem != nil {
			allPoems = append(allPoems, poem)
		}
	}

	if len(allPoems) == 0 {
		return nil
	}

	// Worker completion order is nondeterministic. Sort by the canonical-derived
	// public ID, full canonical ID, and source locator before persistence. The
	// locator tie-breaker makes duplicate-witness aggregation independent of which
	// worker completes first; repository preflight separately rejects any product
	// field disagreement for the same canonical ID.
	sort.Slice(allPoems, func(i, j int) bool {
		left, right := allPoems[i], allPoems[j]
		if left.ID != right.ID {
			return left.ID < right.ID
		}
		leftCanonical, rightCanonical := "", ""
		if left.CanonicalID != nil {
			leftCanonical = *left.CanonicalID
		}
		if right.CanonicalID != nil {
			rightCanonical = *right.CanonicalID
		}
		if leftCanonical != rightCanonical {
			return leftCanonical < rightCanonical
		}
		return firstSourceLocator(left) < firstSourceLocator(right)
	})

	// 任一来源处理失败时禁止写出一个“看似成功但缺 witness”的成品集合。
	// resultCh 已被完整消费，因此不会阻塞 worker；这里只终止数据库产品写入。
	if processingErrors != nil && processingErrors.Load() > 0 {
		logger.Warn("Skipping poem insertion because source records failed processing",
			zap.Int64("failed_sources", processingErrors.Load()),
		)
		return nil
	}

	logger.Info("Batch inserter starting", zap.Int("poems", len(allPoems)))

	// 写入阶段单独用一个进度条容器
	progress := mpb.New(
		mpb.WithWidth(60),
		mpb.WithRefreshRate(100*time.Millisecond),
	)

	// 每个事务写入 2 万首以减少 fsync 次数，
	// 事务内部再按当前配置的 batchSize 分批 INSERT
	transactionSize := 20000

	err := p.repo.BatchInsertPoemsWithTransaction(allPoems, transactionSize, p.batchSize, progress)

	progress.Wait() // 等待进度条渲染结束

	if err != nil {
		return fmt.Errorf("failed to insert poems with transactions: %w", err)
	}

	logger.Info("Batch insertion complete", zap.Int("inserted", len(allPoems)))
	return nil
}

func firstSourceLocator(poem *database.Poem) string {
	if poem == nil || len(poem.Sources) == 0 {
		return ""
	}
	locator := poem.Sources[0].LocatorID
	for _, source := range poem.Sources[1:] {
		if source.LocatorID < locator {
			locator = source.LocatorID
		}
	}
	return locator
}

// resolveTitleByCategory 依据诗词类别决定最终标题，不同类别取自不同的源字段：
//   - 词：取词牌名（rhythmic），若另有标题则拼成「词牌名·副标题」
//   - 论语 / 四书五经：取章节名（chapter）
//   - 其余（诗、曲、诗经、楚辞、蒙学等）：直接取标题
func resolveTitleByCategory(poem loader.PoemData, category string) string {
	switch category {
	case "词", "宋词": // 宋词、五代词，以词牌名为主标题
		if poem.Rhythmic != "" {
			if poem.Title != "" && poem.Title != poem.Rhythmic {
				return poem.Rhythmic + "·" + poem.Title
			}
			return poem.Rhythmic
		}
		return poem.Title // 无词牌名时回退到标题

	case "论语", "四书五经":
		if poem.Chapter != "" {
			return poem.Chapter
		}
		return poem.Title // 无章节名时回退到标题

	default: // 诗、元曲、诗经、楚辞、蒙学等
		return poem.Title
	}
}

// processPoem 把单条原始数据加工成可入库的 Poem，
// 返回 (nil, nil) 表示该条目应被静默跳过。

func (p *Processor) processPoem(work PoemWork) (*database.Poem, error) {
	sourcePoem := work.PoemData

	// Normalize source text before the quality boundary. Paragraph normalization
	// also splits merged sentences (for example "A。B。" into two entries).
	sourceAuthor := classifier.NormalizeText(sourcePoem.Author)
	sourceParagraphs := classifier.NormalizeAndSplitParagraphs(sourcePoem.Paragraphs)
	sourceTitle := classifier.NormalizeText(sourcePoem.Title)
	sourceChapter := classifier.NormalizeText(sourcePoem.Chapter)
	sourceRhythmic := classifier.NormalizeText(sourcePoem.Rhythmic)
	sourceDynasty := classifier.NormalizeText(work.Dynasty)

	// 每个加载出的 source locator 都必须落库或让构建显式失败，不能静默跳过。
	if len(sourceParagraphs) == 0 {
		return nil, fmt.Errorf("source record has no usable content")
	}

	// 占位正文也属于被拒绝的源记录，必须显式报告。
	if classifier.IsPlaceholderContent(sourceParagraphs) {
		return nil, fmt.Errorf("source record contains placeholder content")
	}

	if sourceAuthor == "" {
		sourceAuthor = "佚名"
	}

	// Canonical v2 first converts every identity-affecting field into one fixed
	// simplified script. Classification, title selection, identity, and both
	// localized products all derive from this representation. This collapses the
	// same work when one upstream witness is simplified and another traditional,
	// while preserving both raw source locators in the provenance tables.
	canonicalAuthor, err := canonicalSimplifiedText(sourceAuthor)
	if err != nil {
		return nil, fmt.Errorf("failed to canonicalize author: %w", err)
	}
	canonicalParagraphs, err := canonicalSimplifiedParagraphs(sourceParagraphs)
	if err != nil {
		return nil, fmt.Errorf("failed to canonicalize paragraphs: %w", err)
	}
	canonicalTitle, err := canonicalSimplifiedText(sourceTitle)
	if err != nil {
		return nil, fmt.Errorf("failed to canonicalize title: %w", err)
	}
	canonicalChapter, err := canonicalSimplifiedText(sourceChapter)
	if err != nil {
		return nil, fmt.Errorf("failed to canonicalize chapter: %w", err)
	}
	canonicalRhythmic, err := canonicalSimplifiedText(sourceRhythmic)
	if err != nil {
		return nil, fmt.Errorf("failed to canonicalize rhythmic: %w", err)
	}
	canonicalDynasty, err := canonicalSimplifiedText(sourceDynasty)
	if err != nil {
		return nil, fmt.Errorf("failed to canonicalize dynasty: %w", err)
	}

	canonicalPoem := loader.PoemData{
		Title:      canonicalTitle,
		Chapter:    canonicalChapter,
		Author:     canonicalAuthor,
		Paragraphs: canonicalParagraphs,
		Rhythmic:   canonicalRhythmic,
	}
	typeInfo := classifier.ClassifyPoetryTypeWithDataset(
		canonicalParagraphs, canonicalRhythmic, work.DatasetKey, canonicalTitle,
	)
	canonicalFinalTitle := classifier.NormalizeText(resolveTitleByCategory(canonicalPoem, typeInfo.Category))
	canonicalTypeName, err := canonicalSimplifiedText(typeInfo.TypeName)
	if err != nil {
		return nil, fmt.Errorf("failed to canonicalize poetry type: %w", err)
	}

	// SHA-512 commits the same v2 input independently and lets persistence fail
	// closed if a SHA-256 canonical ID is ever reused for different work text.
	canonicalID, canonicalFingerprint := canonicalIdentity(
		canonicalDynasty, canonicalAuthor, canonicalFinalTitle, canonicalParagraphs,
	)
	poemID, err := stablePoemID(canonicalID)
	if err != nil {
		return nil, fmt.Errorf("failed to derive stable poem ID: %w", err)
	}
	witness, err := sourceWitness(work.PoemWithMeta)
	if err != nil {
		return nil, fmt.Errorf("invalid source provenance: %w", err)
	}

	// Hans is the canonical representation itself. Hant is derived from that same
	// representation, never chosen from whichever source witness a worker happens
	// to finish first.
	author := canonicalAuthor
	paragraphs := canonicalParagraphs
	dynastyName := canonicalDynasty
	typeName := canonicalTypeName
	finalTitle := canonicalFinalTitle
	if p.convertToTraditional {
		author, err = classifier.ToTraditional(canonicalAuthor)
		if err != nil {
			return nil, fmt.Errorf("failed to localize author: %w", err)
		}
		paragraphs, err = classifier.ToTraditionalArray(canonicalParagraphs)
		if err != nil {
			return nil, fmt.Errorf("failed to localize paragraphs: %w", err)
		}
		dynastyName, err = classifier.ToTraditional(canonicalDynasty)
		if err != nil {
			return nil, fmt.Errorf("failed to localize dynasty: %w", err)
		}
		typeName, err = classifier.ToTraditional(canonicalTypeName)
		if err != nil {
			return nil, fmt.Errorf("failed to localize poetry type: %w", err)
		}
		finalTitle, err = classifier.ToTraditional(canonicalFinalTitle)
		if err != nil {
			return nil, fmt.Errorf("failed to localize final title: %w", err)
		}
	}

	dynastyID, err := p.repo.GetOrCreateDynasty(dynastyName)
	if err != nil {
		return nil, fmt.Errorf("failed to get/create dynasty: %w", err)
	}

	canonicalAuthorID, err := database.NewCanonicalAuthorID(canonicalDynasty, canonicalAuthor)
	if err != nil {
		return nil, fmt.Errorf("failed to derive canonical author identity: %w", err)
	}
	authorID, err := p.repo.GetOrCreateCanonicalAuthor(canonicalAuthorID, author, dynastyID)
	if err != nil {
		return nil, fmt.Errorf("failed to get/create author: %w", err)
	}

	typeID, err := p.repo.GetPoetryTypeID(typeName)
	if err != nil {
		return nil, fmt.Errorf("failed to get poetry type: %w", err)
	}

	// 正文以 JSON 数组形式存储
	contentJSON, err := json.Marshal(paragraphs)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal paragraphs: %w", err)
	}

	// 计算正文哈希用于去重。
	// 这里对拼接后的纯文本取哈希（而非 JSON 字节），
	// 这样原本被合并成一句的正文（"A。B。"）在归一化后
	// 与正确拆分的版本（["A。","B。"]）能得到相同的哈希值。
	joinedText := strings.Join(paragraphs, "")
	hash := sha256.Sum256([]byte(joinedText))
	contentHash := hex.EncodeToString(hash[:])

	dbPoem := &database.Poem{
		ID:                   poemID,
		Title:                finalTitle, // 按类别选出的标题，可能来自 title/rhythmic/chapter
		AuthorID:             &authorID,
		DynastyID:            &dynastyID,
		TypeID:               &typeID,
		Content:              datatypes.JSON(contentJSON),
		ContentHash:          contentHash,
		CanonicalID:          &canonicalID,
		CanonicalFingerprint: &canonicalFingerprint,
		Sources:              []database.PoemSource{witness},
	}

	return dbPoem, nil
}

// canonicalSimplifiedText is the single script-normalization boundary for v2
// work identity. Keep whitespace normalization on both sides so a converter
// implementation cannot accidentally introduce identity-relevant edge spaces.
func canonicalSimplifiedText(text string) (string, error) {
	converted, err := classifier.ToSimplified(classifier.NormalizeText(text))
	if err != nil {
		return "", err
	}
	return classifier.NormalizeText(converted), nil
}

func canonicalSimplifiedParagraphs(paragraphs []string) ([]string, error) {
	normalized := classifier.NormalizeAndSplitParagraphs(paragraphs)
	converted, err := classifier.ToSimplifiedArray(normalized)
	if err != nil {
		return nil, err
	}
	return classifier.NormalizeAndSplitParagraphs(converted), nil
}

// convertText 按 toTraditional 标志把文本转为繁体或简体。
func (p *Processor) convertText(text string, toTraditional bool) (string, error) {
	if toTraditional {
		return classifier.ToTraditional(text)
	}
	return classifier.ToSimplified(text)
}
