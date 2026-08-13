package processor

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vbauerster/mpb/v8"

	"github.com/ericismyeldestson/chinese-poetry-api/internal/database"
	"github.com/ericismyeldestson/chinese-poetry-api/internal/loader"
)

func TestCanonicalIdentityGoldenVector(t *testing.T) {
	id, fingerprint := canonicalIdentity("唐", "李白", "静夜思", []string{
		"床前明月光，", "疑是地上霜。", "举头望明月，", "低头思故乡。",
	})
	const wantID = "cpa:poem:v2:sha256:efda2b33b272efd736aa9dc5744a972acba2514002de0c1ab4691711845b9402"
	const wantFingerprint = "04d9ab24f68c6c7370aab83a879fc7d958e25a10ce6f24fcc297bf91eefad0024fb64b191c06b9deb21b65a89788214f2617fb06f2e8fecd62b0c251afbc1908"
	if id != wantID || fingerprint != wantFingerprint {
		t.Fatalf("identity drifted:\nid=%s\nfingerprint=%s", id, fingerprint)
	}

	stableID, err := stablePoemID(id)
	if err != nil {
		t.Fatalf("stablePoemID: %v", err)
	}
	if stableID != 7365850431680471 {
		t.Fatalf("stable ID = %d, want 7365850431680471", stableID)
	}
	if stableID <= 0 || stableID > (1<<53)-1 {
		t.Fatalf("stable ID %d is outside JavaScript's safe integer range", stableID)
	}
}

func TestPinnedSourceAccounting(t *testing.T) {
	if os.Getenv("RUN_FULL_SOURCE_TEST") != "1" {
		t.Skip("set RUN_FULL_SOURCE_TEST=1 to scan the pinned poetry-data submodule")
	}
	configPath := filepath.Join("..", "..", "poetry-data", "loader", "datas.json")
	jsonLoader, err := loader.NewJSONLoader(configPath)
	if err != nil {
		t.Fatalf("NewJSONLoader(%s): %v", configPath, err)
	}
	records, err := jsonLoader.LoadAll()
	if err != nil {
		t.Fatalf("LoadAll pinned source: %v", err)
	}
	accepted, rejected, err := PartitionSources(records)
	if err != nil {
		t.Fatalf("PartitionSources pinned source: %v", err)
	}
	report, err := jsonLoader.FinalizeReport(records)
	if err != nil {
		t.Fatalf("FinalizeReport pinned source: %v", err)
	}
	if report.Totals.TotalRecords != len(records) || report.Totals.AcceptedRecords != len(accepted) ||
		report.Totals.RejectedRecords != len(rejected) {
		t.Fatalf("accounting mismatch: records/accepted/rejected=%d/%d/%d report=%#v",
			len(records), len(accepted), len(rejected), report.Totals)
	}
	reasons := make(map[string]int)
	for _, rejection := range rejected {
		reasons[rejection.Stage+"/"+rejection.Reason]++
	}
	t.Logf("pinned source: files=%d excluded_files=%d records=%d accepted=%d rejected=%d reasons=%v",
		len(report.Files), report.Totals.ExcludedFiles, len(records), len(accepted), len(rejected), reasons)
}

func TestCanonicalIdentityPreservesFieldAndParagraphBoundaries(t *testing.T) {
	oneParagraph, _ := canonicalIdentity("唐", "甲", "乙", []string{"甲乙"})
	twoParagraphs, _ := canonicalIdentity("唐", "甲", "乙", []string{"甲", "乙"})
	differentFields, _ := canonicalIdentity("唐甲", "乙", "", []string{"甲乙"})
	if oneParagraph == twoParagraphs || oneParagraph == differentFields || twoParagraphs == differentFields {
		t.Fatal("length-prefixed canonical encoding aliased distinct structures")
	}
}

func TestCanonicalIdentityVersionRejectsV1Prefix(t *testing.T) {
	v1ID := "cpa:poem:v1:sha256:" + strings.Repeat("0", 64)
	if _, err := database.PoemIDFromCanonical(v1ID); err == nil {
		t.Fatal("PoemIDFromCanonical accepted a canonical v1 identity")
	}
}

func TestHansHantAndUnrelatedPrefixKeepPoemIdentity(t *testing.T) {
	meta := loader.PoemWithMeta{
		PoemData: loader.PoemData{
			ID:         "source-1",
			Title:      "後庭花",
			Author:     "張三",
			Paragraphs: []string{"雲裡故鄉。"},
		},
		Dynasty:           "宋",
		DatasetName:       "测试",
		DatasetKey:        "tangsong",
		SourceID:          "source-1",
		SourcePath:        "全唐诗/poet.song.0.json",
		SourceRecordIndex: 0,
	}

	hans := &Processor{repo: &identityTestRepository{}, convertToTraditional: false}
	hant := &Processor{repo: &identityTestRepository{}, convertToTraditional: true}
	hansPoem, err := hans.processPoem(PoemWork{PoemWithMeta: meta, SourceOrdinal: 1})
	if err != nil {
		t.Fatalf("Hans processPoem: %v", err)
	}
	hantPoem, err := hant.processPoem(PoemWork{PoemWithMeta: meta, SourceOrdinal: 1})
	if err != nil {
		t.Fatalf("Hant processPoem: %v", err)
	}
	// Simulate an unrelated source being inserted before this record. The source
	// ordinal is intentionally different; the API-facing ID remains canonical.
	afterPrefixInsert, err := hans.processPoem(PoemWork{PoemWithMeta: meta, SourceOrdinal: 999})
	if err != nil {
		t.Fatalf("process after prefix insert: %v", err)
	}

	if hansPoem.CanonicalID == nil || hantPoem.CanonicalID == nil || afterPrefixInsert.CanonicalID == nil {
		t.Fatal("canonical identity was not populated")
	}
	if *hansPoem.CanonicalID != *hantPoem.CanonicalID || *hansPoem.CanonicalID != *afterPrefixInsert.CanonicalID {
		t.Fatalf("canonical IDs differ: Hans=%q Hant=%q prefixed=%q",
			*hansPoem.CanonicalID, *hantPoem.CanonicalID, *afterPrefixInsert.CanonicalID)
	}
	if hansPoem.ID != hantPoem.ID || hansPoem.ID != afterPrefixInsert.ID {
		t.Fatalf("stable public IDs differ: Hans=%d Hant=%d prefixed=%d", hansPoem.ID, hantPoem.ID, afterPrefixInsert.ID)
	}
	if hansPoem.ID > (1<<53)-1 {
		t.Fatalf("public ID %d exceeds JavaScript safe integer range", hansPoem.ID)
	}
}

func TestSimplifiedAndTraditionalWitnessesConvergeToOneProduct(t *testing.T) {
	simplified := loader.PoemWithMeta{
		PoemData: loader.PoemData{
			ID:         "simplified-source",
			Title:      "后庭花",
			Author:     "张三",
			Paragraphs: []string{"云里故乡。"},
		},
		Dynasty:           "宋",
		DatasetName:       "测试",
		DatasetKey:        "tangsong",
		SourceID:          "simplified-source",
		SourcePath:        "全唐诗/poet.song.0.json",
		SourceRecordIndex: 0,
	}
	traditional := simplified
	traditional.PoemData = loader.PoemData{
		ID:         "traditional-source",
		Title:      "後庭花",
		Author:     "張三",
		Paragraphs: []string{"雲裡故鄉。"},
	}
	traditional.SourceID = "traditional-source"
	traditional.SourceRecordIndex = 1

	for _, test := range []struct {
		name        string
		traditional bool
	}{
		{name: "zh-Hans"},
		{name: "zh-Hant", traditional: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			processor := &Processor{
				repo:                 &identityTestRepository{},
				convertToTraditional: test.traditional,
			}
			fromSimplified, err := processor.processPoem(PoemWork{PoemWithMeta: simplified, SourceOrdinal: 1})
			if err != nil {
				t.Fatalf("process simplified witness: %v", err)
			}
			fromTraditional, err := processor.processPoem(PoemWork{PoemWithMeta: traditional, SourceOrdinal: 2})
			if err != nil {
				t.Fatalf("process traditional witness: %v", err)
			}

			if fromSimplified.CanonicalID == nil || fromTraditional.CanonicalID == nil ||
				*fromSimplified.CanonicalID != *fromTraditional.CanonicalID {
				t.Fatalf("canonical IDs differ: simplified=%v traditional=%v",
					fromSimplified.CanonicalID, fromTraditional.CanonicalID)
			}
			if fromSimplified.CanonicalFingerprint == nil || fromTraditional.CanonicalFingerprint == nil ||
				*fromSimplified.CanonicalFingerprint != *fromTraditional.CanonicalFingerprint {
				t.Fatal("canonical fingerprints differ")
			}
			if fromSimplified.ID != fromTraditional.ID ||
				fromSimplified.Title != fromTraditional.Title ||
				string(fromSimplified.Content) != string(fromTraditional.Content) ||
				fromSimplified.ContentHash != fromTraditional.ContentHash ||
				*fromSimplified.TypeID != *fromTraditional.TypeID ||
				*fromSimplified.AuthorID != *fromTraditional.AuthorID ||
				*fromSimplified.DynastyID != *fromTraditional.DynastyID {
				t.Fatalf("localized products differ:\nsimplified=%#v\ntraditional=%#v",
					fromSimplified, fromTraditional)
			}
		})
	}
}

func TestBatchInserterUsesStableCanonicalOrder(t *testing.T) {
	repo := &orderingTestRepository{}
	processor := &Processor{repo: repo, batchSize: 10}
	resultCh := make(chan *database.Poem, 4)
	resultCh <- orderingTestPoem(2, "canonical-b", "locator-a")
	resultCh <- orderingTestPoem(1, "canonical-z", "locator-m")
	resultCh <- orderingTestPoem(1, "canonical-a", "locator-z")
	resultCh <- orderingTestPoem(1, "canonical-a", "locator-a")
	close(resultCh)

	if err := processor.batchInserter(resultCh, nil); err != nil {
		t.Fatalf("batchInserter: %v", err)
	}
	want := []string{
		"canonical-a/locator-a",
		"canonical-a/locator-z",
		"canonical-z/locator-m",
		"canonical-b/locator-a",
	}
	if len(repo.poems) != len(want) {
		t.Fatalf("captured %d poems, want %d", len(repo.poems), len(want))
	}
	for i, poem := range repo.poems {
		got := *poem.CanonicalID + "/" + poem.Sources[0].LocatorID
		if got != want[i] {
			t.Errorf("order[%d] = %q, want %q", i, got, want[i])
		}
	}
}

func orderingTestPoem(id int64, canonicalID, locator string) *database.Poem {
	return &database.Poem{
		ID:          id,
		CanonicalID: &canonicalID,
		Sources:     []database.PoemSource{{LocatorID: locator}},
	}
}

func TestPartitionSourcesAuditsQualityRejections(t *testing.T) {
	records := []loader.PoemWithMeta{
		testSourceRecord(0, "正常", []string{"可用正文。"}),
		testSourceRecord(1, "缺正文", nil),
		testSourceRecord(2, "占位", []string{"无正文。"}),
		testSourceRecord(3, "乱码�", []string{"正文。"}),
		testSourceRecord(4, "纯标点", []string{"……"}),
		testSourceRecord(5, "章节乱码", []string{"正文。"}),
		testSourceRecord(6, "词牌乱码", []string{"正文。"}),
	}
	records[1].RejectionStage = "loader"
	records[1].RejectionReason = "missing_content"
	records[5].Chapter = "第�章"
	records[6].Rhythmic = "水调�头"

	accepted, rejected, err := PartitionSources(records)
	if err != nil {
		t.Fatalf("PartitionSources: %v", err)
	}
	if len(accepted) != 1 || len(rejected) != 6 {
		t.Fatalf("accepted/rejected = %d/%d, want 1/6", len(accepted), len(rejected))
	}
	wantReasons := []string{
		"missing_content", "placeholder_content", "unicode_replacement_character", "empty_after_normalization",
		"unicode_replacement_character", "unicode_replacement_character",
	}
	for i, reason := range wantReasons {
		if rejected[i].Reason != reason || rejected[i].LocatorID == "" {
			t.Errorf("rejection %d = %#v, want reason %q and locator", i, rejected[i], reason)
		}
	}
}

func testSourceRecord(index int, title string, paragraphs []string) loader.PoemWithMeta {
	return loader.PoemWithMeta{
		PoemData: loader.PoemData{Title: title, Author: "作者", Paragraphs: paragraphs},
		Dynasty:  "唐", DatasetKey: "tangsong", SourcePath: "全唐诗/poet.tang.0.json", SourceRecordIndex: index,
	}
}

// Embedding the interface supplies unused repository methods. processPoem only
// calls the four deterministic methods overridden below.
type identityTestRepository struct {
	database.RepositoryInterface
}

func (*identityTestRepository) GetOrCreateDynasty(string) (int64, error) { return 1, nil }
func (*identityTestRepository) GetOrCreateAuthor(string, int64) (int64, error) {
	return 2, nil
}

func (*identityTestRepository) GetOrCreateCanonicalAuthor(string, string, int64) (int64, error) {
	return 2, nil
}
func (*identityTestRepository) GetPoetryTypeID(string) (int64, error) { return 3, nil }

type orderingTestRepository struct {
	database.RepositoryInterface
	poems []*database.Poem
	err   error
}

func (r *orderingTestRepository) BatchInsertPoemsWithTransaction(
	poems []*database.Poem, _, _ int, _ *mpb.Progress,
) error {
	r.poems = append(r.poems, poems...)
	return r.err
}

func TestBatchInserterDoesNotHangWhenRepositoryWriteFails(t *testing.T) {
	want := errors.New("injected database failure")
	repo := &orderingTestRepository{err: want}
	processor := &Processor{repo: repo, batchSize: 1}
	resultCh := make(chan *database.Poem, 1)
	resultCh <- &database.Poem{ID: 1}
	close(resultCh)

	done := make(chan error, 1)
	go func() { done <- processor.batchInserter(resultCh, nil) }()
	select {
	case err := <-done:
		if !errors.Is(err, want) {
			t.Fatalf("batchInserter error = %v, want wrapped %v", err, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("batchInserter hung after repository write failure")
	}
}
