package loader

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestJSONLoaderStableOrderingDynastyAndProvenance(t *testing.T) {
	root := t.TempDir()
	loaderDir := filepath.Join(root, "loader")
	tangSongDir := filepath.Join(root, "全唐诗")
	mustMkdirAll(t, loaderDir)
	mustMkdirAll(t, tangSongDir)

	configPath := filepath.Join(loaderDir, "datas.json")
	mustWriteFile(t, configPath, `{
  "cp_path":"./",
  "datasets":{
    "yuanqu":{"name":"元曲","id":2,"path":"元曲.json","tag":"paragraphs"},
    "tangsong":{"name":"全唐诗全宋诗","id":3,"path":"全唐诗","tag":"paragraphs"}
  }
}`)
	mustWriteFile(t, filepath.Join(tangSongDir, "poet.tang.0.json"),
		`[{"id":"tang-source-id","title":"唐作","author":"唐人","paragraphs":["唐诗正文。"]}]`)
	mustWriteFile(t, filepath.Join(tangSongDir, "poet.song.0.json"),
		`[{"id":"song-source-id","title":"宋作","author":"宋人","paragraphs":["宋诗正文。"]}]`)
	mustWriteFile(t, filepath.Join(root, "元曲.json"),
		`[{"id":"yuan-source-id","title":"元作","author":"元人","paragraphs":["元曲正文。"]}]`)

	loader, err := NewJSONLoader(configPath)
	if err != nil {
		t.Fatalf("NewJSONLoader: %v", err)
	}
	first, err := loader.LoadAll()
	if err != nil {
		t.Fatalf("first LoadAll: %v", err)
	}
	second, err := loader.LoadAll()
	if err != nil {
		t.Fatalf("second LoadAll: %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("consecutive loads differ:\nfirst=%#v\nsecond=%#v", first, second)
	}

	if len(first) != 3 {
		t.Fatalf("got %d poems, want 3", len(first))
	}
	// Dataset keys are sorted, then source filenames are sorted explicitly.
	wantKeys := []string{"tangsong", "tangsong", "yuanqu"}
	wantDynasties := []string{"宋", "唐", "元"}
	wantPaths := []string{"全唐诗/poet.song.0.json", "全唐诗/poet.tang.0.json", "元曲.json"}
	wantSourceIDs := []string{"song-source-id", "tang-source-id", "yuan-source-id"}
	for i, poem := range first {
		if poem.DatasetKey != wantKeys[i] || poem.Dynasty != wantDynasties[i] ||
			poem.SourcePath != wantPaths[i] || poem.SourceID != wantSourceIDs[i] ||
			poem.SourceRecordIndex != 0 {
			t.Errorf("poem %d metadata = %#v", i, poem)
		}
	}
}

func TestJSONLoaderTangSongFailsClosed(t *testing.T) {
	tests := []struct {
		name     string
		fileName string
		content  string
		wantText string
	}{
		{
			name:     "unclassifiable filename",
			fileName: "poet.unknown.0.json",
			content:  `[{"title":"无法判定","paragraphs":["正文。"]}]`,
			wantText: "cannot determine dynasty",
		},
		{
			name:     "malformed JSON",
			fileName: "poet.song.0.json",
			content:  `[{`,
			wantText: "failed to parse JSON",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			loaderDir := filepath.Join(root, "loader")
			dataDir := filepath.Join(root, "全唐诗")
			mustMkdirAll(t, loaderDir)
			mustMkdirAll(t, dataDir)
			mustWriteFile(t, filepath.Join(loaderDir, "datas.json"), `{
  "cp_path":"./",
  "datasets":{"tangsong":{"name":"全唐诗全宋诗","id":3,"path":"全唐诗","tag":"paragraphs"}}
}`)
			mustWriteFile(t, filepath.Join(dataDir, test.fileName), test.content)

			loader, err := NewJSONLoader(filepath.Join(loaderDir, "datas.json"))
			if err != nil {
				t.Fatalf("NewJSONLoader: %v", err)
			}
			_, err = loader.LoadAll()
			if err == nil || !strings.Contains(err.Error(), test.wantText) {
				t.Fatalf("LoadAll error = %v, want substring %q", err, test.wantText)
			}
		})
	}
}

func TestJSONLoaderRetainsRejectedSourceRecord(t *testing.T) {
	root := t.TempDir()
	loaderDir := filepath.Join(root, "loader")
	dataDir := filepath.Join(root, "全唐诗")
	mustMkdirAll(t, loaderDir)
	mustMkdirAll(t, dataDir)
	mustWriteFile(t, filepath.Join(loaderDir, "datas.json"), `{
  "cp_path":"./",
  "datasets":{"tangsong":{"name":"全唐诗全宋诗","id":3,"path":"全唐诗","tag":"paragraphs"}}
}`)
	mustWriteFile(t, filepath.Join(dataDir, "poet.song.0.json"),
		`[{"id":"empty-source","title":"空记录"},{"id":"valid-source","title":"有效","paragraphs":["正文。"]}]`)

	loader, err := NewJSONLoader(filepath.Join(loaderDir, "datas.json"))
	if err != nil {
		t.Fatalf("NewJSONLoader: %v", err)
	}
	poems, err := loader.LoadAll()
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if len(poems) != 2 {
		t.Fatalf("got %d records, want 2", len(poems))
	}
	if poems[0].SourceRecordIndex != 0 || poems[0].RejectionStage != "loader" || poems[0].RejectionReason != "missing_content" {
		t.Fatalf("rejected record not retained: %#v", poems[0])
	}
	if poems[1].SourceRecordIndex != 1 || poems[1].RejectionReason != "" {
		t.Fatalf("valid record metadata wrong: %#v", poems[1])
	}
	report := loader.Report()
	if report.Totals.TotalRecords != 2 || report.Totals.AcceptedRecords != 1 || report.Totals.RejectedRecords != 1 {
		t.Fatalf("report = %#v", report)
	}
}

func mustMkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", path, err)
	}
}

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile(%s): %v", path, err)
	}
}
