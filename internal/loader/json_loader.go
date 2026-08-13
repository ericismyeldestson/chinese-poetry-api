package loader

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// DataConfig 对应 datas.json 的结构。
type DataConfig struct {
	CPPath   string                 `json:"cp_path"`
	Datasets map[string]DatasetInfo `json:"datasets"`
}

// DatasetInfo 描述单个数据集的配置。
type DatasetInfo struct {
	Name     string   `json:"name"`
	ID       int      `json:"id"`
	Path     string   `json:"path"`
	Tag      string   `json:"tag"`
	Excludes []string `json:"excludes"`
	Comments string   `json:"comments,omitempty"`
}

// PoemData 是 JSON 文件中的一首诗词。
type PoemData struct {
	ID         string   `json:"id"`
	Title      string   `json:"title"`
	Chapter    string   `json:"chapter,omitempty"` // 用于论语、四书五经
	Author     string   `json:"author"`
	Paragraphs []string `json:"paragraphs"`
	Rhythmic   string   `json:"rhythmic,omitempty"` // 词牌名，用于词
	Content    string   `json:"content,omitempty"`  // 正文的备用字段
	Para       []string `json:"para,omitempty"`     // 正文的备用字段
}

// JSONLoader 从 JSON 文件中加载诗词数据。
type JSONLoader struct {
	config   *DataConfig
	basePath string
	report   LoadReport
}

// LoadReport is a deterministic manifest of every JSON source file considered
// by the loader and every upstream record accepted or rejected.
type LoadReport struct {
	SchemaVersion int                  `json:"schema_version"`
	Files         []SourceFileDecision `json:"files"`
	Totals        LoadReportTotals     `json:"totals"`
}

type LoadReportTotals struct {
	TotalRecords    int `json:"total_records"`
	AcceptedRecords int `json:"accepted_records"`
	RejectedRecords int `json:"rejected_records"`
	ExcludedFiles   int `json:"excluded_files"`
}

// SourceFileDecision records why a candidate JSON file was loaded or excluded.
type SourceFileDecision struct {
	DatasetKey      string `json:"dataset_key"`
	SourcePath      string `json:"source_path"`
	Action          string `json:"action"`
	Reason          string `json:"reason,omitempty"`
	TotalRecords    int    `json:"total_records"`
	AcceptedRecords int    `json:"accepted_records"`
	RejectedRecords int    `json:"rejected_records"`
}

// NewJSONLoader 读取配置文件并创建加载器。
func NewJSONLoader(configPath string) (*JSONLoader, error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var config DataConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}

	// 确定数据文件的根目录
	configDir := filepath.Dir(configPath)
	var basePath string

	// cp_path 指定了非当前目录时，与配置文件所在目录拼接
	if config.CPPath != "" && config.CPPath != "./" && config.CPPath != "." {
		basePath = filepath.Join(configDir, config.CPPath)
	} else {
		// cp_path 为 "./" 或 "." 时，根目录取 loader 目录的上一级
		basePath = filepath.Dir(configDir)
	}

	return &JSONLoader{
		config:   &config,
		basePath: filepath.Clean(basePath),
	}, nil
}

// LoadAll 加载全部数据集中的诗词数据。
func (l *JSONLoader) LoadAll() ([]PoemWithMeta, error) {
	var allPoems []PoemWithMeta
	l.report = LoadReport{SchemaVersion: 1}

	// Go map 的遍历顺序不稳定。先按 key 排序，保证相同输入在每次构建中
	// 都产生相同的诗词顺序，进而让兼容用的顺序整数 ID 保持稳定。
	keys := make([]string, 0, len(l.config.Datasets))
	for key := range l.config.Datasets {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	for _, key := range keys {
		dataset := l.config.Datasets[key]
		poems, err := l.loadDataset(key, dataset)
		if err != nil {
			return nil, fmt.Errorf("failed to load dataset %s: %w", key, err)
		}
		allPoems = append(allPoems, poems...)
	}
	sort.Slice(l.report.Files, func(i, j int) bool {
		if l.report.Files[i].DatasetKey != l.report.Files[j].DatasetKey {
			return l.report.Files[i].DatasetKey < l.report.Files[j].DatasetKey
		}
		return l.report.Files[i].SourcePath < l.report.Files[j].SourcePath
	})

	return allPoems, nil
}

// Report returns a copy of the manifest produced by the most recent LoadAll.
func (l *JSONLoader) Report() LoadReport {
	report := l.report
	report.Files = append([]SourceFileDecision(nil), l.report.Files...)
	return report
}

// PoemWithMeta 是诗词数据加上其来源信息。
type PoemWithMeta struct {
	PoemData
	Dynasty           string
	DatasetName       string
	DatasetKey        string
	SourceID          string
	SourcePath        string
	SourceRecordIndex int
	RejectionStage    string
	RejectionReason   string
}

type poemFileRecord struct {
	PoemData
	SourceRecordIndex int
	RejectionStage    string
	RejectionReason   string
}

func (l *JSONLoader) loadDataset(key string, dataset DatasetInfo) ([]PoemWithMeta, error) {
	fullPath := filepath.Join(l.basePath, dataset.Path)

	info, err := os.Stat(fullPath)
	if err != nil {
		return nil, fmt.Errorf("failed to stat path %s: %w", fullPath, err)
	}

	var poems []PoemWithMeta

	if info.IsDir() {
		// 目录：逐个加载其中的 JSON 文件
		entries, err := os.ReadDir(fullPath)
		if err != nil {
			return nil, fmt.Errorf("failed to read directory %s: %w", fullPath, err)
		}

		// os.ReadDir 当前承诺按文件名排序，但这里仍显式排序候选路径，
		// 把构建身份的稳定性写进本加载器自己的契约中。
		var filePaths []string
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}

			if filepath.Ext(entry.Name()) != ".json" {
				continue
			}

			filePath := filepath.Join(fullPath, entry.Name())
			sourcePath, err := l.sourcePath(filePath)
			if err != nil {
				return nil, err
			}
			if contains(dataset.Excludes, entry.Name()) {
				l.recordExcludedFile(key, sourcePath, "listed in dataset excludes")
				continue
			}
			if reason, excluded := builtInSourceExclusion(key, entry.Name()); excluded {
				l.recordExcludedFile(key, sourcePath, reason)
				continue
			}
			filePaths = append(filePaths, filePath)
		}
		sort.Strings(filePaths)

		for _, filePath := range filePaths {
			dynasty, err := inferDynasty(key, dataset.Name, filepath.Base(filePath))
			if err != nil {
				return nil, err
			}
			sourcePath, err := l.sourcePath(filePath)
			if err != nil {
				return nil, err
			}
			filePoems, err := l.loadJSONFile(filePath, dataset.Tag)
			if err != nil {
				return nil, fmt.Errorf("failed to load %s: %w", sourcePath, err)
			}
			l.recordLoadedFile(key, sourcePath, filePoems)

			for _, poem := range filePoems {
				poemWithMeta := PoemWithMeta{
					PoemData:          poem.PoemData,
					Dynasty:           dynasty,
					DatasetName:       dataset.Name,
					DatasetKey:        key,
					SourceID:          poem.ID,
					SourcePath:        sourcePath,
					SourceRecordIndex: poem.SourceRecordIndex,
					RejectionStage:    poem.RejectionStage,
					RejectionReason:   poem.RejectionReason,
				}

				// 数据中没有作者时填入该数据集的默认作者
				if poemWithMeta.Author == "" {
					if defaultAuthor := getDefaultAuthorFromDataset(key); defaultAuthor != "" {
						poemWithMeta.Author = defaultAuthor
					}
				}

				poems = append(poems, poemWithMeta)
			}
		}
	} else {
		// 单个文件：直接加载
		dynasty, err := inferDynasty(key, dataset.Name, filepath.Base(fullPath))
		if err != nil {
			return nil, err
		}
		sourcePath, err := l.sourcePath(fullPath)
		if err != nil {
			return nil, err
		}
		filePoems, err := l.loadJSONFile(fullPath, dataset.Tag)
		if err != nil {
			return nil, err
		}
		l.recordLoadedFile(key, sourcePath, filePoems)

		for _, poem := range filePoems {
			poemWithMeta := PoemWithMeta{
				PoemData:          poem.PoemData,
				Dynasty:           dynasty,
				DatasetName:       dataset.Name,
				DatasetKey:        key,
				SourceID:          poem.ID,
				SourcePath:        sourcePath,
				SourceRecordIndex: poem.SourceRecordIndex,
				RejectionStage:    poem.RejectionStage,
				RejectionReason:   poem.RejectionReason,
			}

			// 数据中没有作者时填入该数据集的默认作者
			if poemWithMeta.Author == "" {
				if defaultAuthor := getDefaultAuthorFromDataset(key); defaultAuthor != "" {
					poemWithMeta.Author = defaultAuthor
				}
			}

			poems = append(poems, poemWithMeta)
		}
	}

	return poems, nil
}

func (l *JSONLoader) sourcePath(path string) (string, error) {
	rel, err := filepath.Rel(l.basePath, path)
	if err != nil {
		return "", fmt.Errorf("failed to derive source path for %s: %w", path, err)
	}
	rel = filepath.ToSlash(filepath.Clean(rel))
	if rel == "." || rel == ".." || strings.HasPrefix(rel, "../") || filepath.IsAbs(rel) {
		return "", fmt.Errorf("source path %q escapes data root", rel)
	}
	return rel, nil
}

func (l *JSONLoader) loadJSONFile(path string, tag string) ([]poemFileRecord, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read file: %w", err)
	}

	var rawPoems []map[string]any
	if err := json.Unmarshal(data, &rawPoems); err != nil {
		return nil, fmt.Errorf("failed to parse JSON: %w", err)
	}

	var poems []poemFileRecord
	for recordIndex, raw := range rawPoems {
		poem := PoemData{
			Title:  getString(raw, "title"),
			Author: getString(raw, "author"),
		}

		// ID 字段
		if id, ok := raw["id"].(string); ok {
			poem.ID = id
		}

		// 词牌名，用于词
		if rhythmic, ok := raw["rhythmic"].(string); ok {
			poem.Rhythmic = rhythmic
		}

		// 章节名，用于论语、四书五经
		if chapter, ok := raw["chapter"].(string); ok {
			poem.Chapter = chapter
		}

		// 按配置的 tag 提取正文
		switch tag {
		case "paragraphs":
			poem.Paragraphs = getStringArray(raw, "paragraphs")
		case "content":
			if content, ok := raw["content"].(string); ok {
				poem.Content = content
				poem.Paragraphs = []string{content}
			} else {
				poem.Paragraphs = getStringArray(raw, "content")
			}
		case "para":
			poem.Paragraphs = getStringArray(raw, "para")
		default:
			// 未指定则依次尝试各个可能的字段
			if paras := getStringArray(raw, "paragraphs"); len(paras) > 0 {
				poem.Paragraphs = paras
			} else if paras := getStringArray(raw, "para"); len(paras) > 0 {
				poem.Paragraphs = paras
			} else if content, ok := raw["content"].(string); ok {
				poem.Paragraphs = []string{content}
			}
		}

		rejectionStage := ""
		rejectionReason := ""
		if len(poem.Paragraphs) == 0 {
			rejectionStage = "loader"
			rejectionReason = "missing_content"
		}
		poems = append(poems, poemFileRecord{
			PoemData:          poem,
			SourceRecordIndex: recordIndex,
			RejectionStage:    rejectionStage,
			RejectionReason:   rejectionReason,
		})
	}

	return poems, nil
}

func (l *JSONLoader) recordExcludedFile(datasetKey, sourcePath, reason string) {
	l.report.Files = append(l.report.Files, SourceFileDecision{
		DatasetKey: datasetKey,
		SourcePath: sourcePath,
		Action:     "excluded",
		Reason:     reason,
	})
	l.report.Totals.ExcludedFiles++
}

func (l *JSONLoader) recordLoadedFile(datasetKey, sourcePath string, records []poemFileRecord) {
	decision := SourceFileDecision{
		DatasetKey:   datasetKey,
		SourcePath:   sourcePath,
		Action:       "loaded",
		TotalRecords: len(records),
	}
	for _, record := range records {
		if record.RejectionReason == "" {
			decision.AcceptedRecords++
		} else {
			decision.RejectedRecords++
		}
	}
	l.report.Files = append(l.report.Files, decision)
	l.report.Totals.TotalRecords += decision.TotalRecords
	l.report.Totals.AcceptedRecords += decision.AcceptedRecords
	l.report.Totals.RejectedRecords += decision.RejectedRecords
}

// FinalizeReport reconciles loader decisions with later, pre-conversion quality
// rejections (for example placeholder content). Every loaded record must still
// correspond to exactly one source occurrence in records.
func (l *JSONLoader) FinalizeReport(records []PoemWithMeta) (LoadReport, error) {
	report := l.Report()
	decisionByPath := make(map[string]*SourceFileDecision)
	for i := range report.Files {
		decision := &report.Files[i]
		if decision.Action == "loaded" {
			decision.AcceptedRecords = 0
			decision.RejectedRecords = 0
			decisionByPath[decision.SourcePath] = decision
		}
	}

	for _, record := range records {
		decision, exists := decisionByPath[record.SourcePath]
		if !exists {
			return LoadReport{}, fmt.Errorf("source record %s#%d has no loaded file decision", record.SourcePath, record.SourceRecordIndex)
		}
		if record.RejectionReason == "" {
			decision.AcceptedRecords++
		} else {
			decision.RejectedRecords++
		}
	}

	report.Totals.TotalRecords = 0
	report.Totals.AcceptedRecords = 0
	report.Totals.RejectedRecords = 0
	for _, decision := range report.Files {
		if decision.Action != "loaded" {
			continue
		}
		if decision.AcceptedRecords+decision.RejectedRecords != decision.TotalRecords {
			return LoadReport{}, fmt.Errorf(
				"file %s accounts for %d of %d source records",
				decision.SourcePath, decision.AcceptedRecords+decision.RejectedRecords, decision.TotalRecords,
			)
		}
		report.Totals.TotalRecords += decision.TotalRecords
		report.Totals.AcceptedRecords += decision.AcceptedRecords
		report.Totals.RejectedRecords += decision.RejectedRecords
	}
	return report, nil
}

func builtInSourceExclusion(datasetKey, sourceFile string) (string, bool) {
	// The pinned upstream Song-Ci tree names its author metadata file in the
	// singular, while datas.json historically excludes the non-existent plural
	// spelling. Keep this exact, reviewable exception fail-closed rather than
	// treating arbitrary JSON with no paragraphs as poetry.
	if datasetKey == "songci" && sourceFile == "author.song.json" {
		return "upstream author metadata (datas.json historically lists authors.song.json)", true
	}
	return "", false
}

var tangSongPoemFile = regexp.MustCompile(`^poet\.(tang|song)\.[0-9]+\.json$`)

func inferDynasty(key, name, sourceFile string) (string, error) {
	// 全唐诗目录同时包含唐诗和宋诗，必须逐文件判定，禁止沿用整目录标唐。
	if key == "tangsong" {
		match := tangSongPoemFile.FindStringSubmatch(sourceFile)
		if len(match) == 2 {
			if match[1] == "tang" {
				return "唐", nil
			}
			return "宋", nil
		}

		// 上游目录中这两个独立选集没有 poet.* 命名，但来源本身明确为唐诗。
		switch sourceFile {
		case "唐诗三百首.json", "唐诗补录.json":
			return "唐", nil
		default:
			return "", fmt.Errorf("cannot determine dynasty for tangsong source file %q", sourceFile)
		}
	}

	// 数据集 key 到朝代的映射
	dynastyMap := map[string]string{
		"songci":            "宋",
		"yuanqu":            "元",
		"wudai-huajianji":   "五代",
		"wudai-nantang":     "五代",
		"yudingquantangshi": "唐",
		"shuimotangshi":     "唐",
		"shijing":           "先秦",
		"chuci":             "先秦",
		"lunyu":             "先秦",
		"mengzi":            "先秦",
		"caocao":            "魏晋",
		"nalanxingde":       "清",
		"youmengying":       "其他",
	}

	if dynasty, ok := dynastyMap[key]; ok {
		return dynasty, nil
	}

	return "", fmt.Errorf("cannot determine dynasty for dataset %q (%s)", key, name)
}

// getDefaultAuthorFromDataset 返回数据集的默认作者，用于数据中缺少 author 字段的情况。
func getDefaultAuthorFromDataset(datasetKey string) string {
	authorMap := map[string]string{
		"caocao":      "曹操",
		"nalanxingde": "纳兰性德",
	}

	if author, ok := authorMap[datasetKey]; ok {
		return author
	}
	return ""
}

func getString(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func getStringArray(m map[string]any, key string) []string {
	if arr, ok := m[key].([]any); ok {
		result := make([]string, 0, len(arr))
		for _, item := range arr {
			if str, ok := item.(string); ok {
				result = append(result, str)
			}
		}
		return result
	}
	return nil
}

func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}
