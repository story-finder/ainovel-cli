package webhost

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/host/exp"
	"github.com/voocel/ainovel-cli/internal/host/imp"
)

func TestWebCommandCatalogMatchesApprovedTUICommands(t *testing.T) {
	want := map[string]struct {
		usage       string
		description string
	}{
		"model": {
			usage:       "/model [vai-trò]",
			description: "Chuyển đổi mô hình mặc định hoặc theo vai trò",
		},
		"diag": {
			usage:       "/diag",
			description: "Chẩn đoán tình trạng sáng tác tiểu thuyết",
		},
		"export": {
			usage:       "/export",
			description: "Xuất truyện các chương đã hoàn thành sang TXT/EPUB",
		},
		"import": {
			usage:       "/import <đường-dẫn>",
			description: "Nhập truyện bên ngoài để tiếp tục viết",
		},
		"simulate": {
			usage:       "/simulate",
			description: "Đọc ./simulate để tạo hoặc cập nhật tăng dần hồ sơ mô phỏng phong cách viết",
		},
		"cocreate": {
			usage:       "/cocreate",
			description: "Tạm dừng sáng tác, đồng sáng tác lên kế hoạch cho các giai đoạn tiếp theo",
		},
	}

	got := commandCatalog()
	if len(got) != len(want) {
		t.Fatalf("catalog length = %d, want %d: %#v", len(got), len(want), got)
	}
	seen := make(map[string]int, len(got))
	for _, item := range got {
		seen[item.Name]++
		if seen[item.Name] > 1 {
			t.Fatalf("/%s appears %d times, want exactly once", item.Name, seen[item.Name])
		}
		expected, ok := want[item.Name]
		if !ok {
			t.Fatalf("unexpected web command /%s", item.Name)
		}
		if item.Usage != expected.usage || item.Description != expected.description {
			t.Fatalf("/%s = usage %q, description %q; want usage %q, description %q", item.Name, item.Usage, item.Description, expected.usage, expected.description)
		}
	}
	for name := range want {
		if seen[name] != 1 {
			t.Fatalf("/%s appears %d times, want exactly once", name, seen[name])
		}
	}
}

func TestParseSlashCommandPreservesTUIArguments(t *testing.T) {
	got, err := parseSlashCommand("/export ~/ten-truyen.epub from=3 to=8 --overwrite")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "export" || !reflect.DeepEqual(got.Args, []string{"~/ten-truyen.epub", "from=3", "to=8", "--overwrite"}) {
		t.Fatalf("parsed command = %#v", got)
	}
}

func TestParseSlashCommandTrimsInputAndLowercasesName(t *testing.T) {
	got, err := parseSlashCommand("  /EXPORT  ")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "export" || len(got.Args) != 0 {
		t.Fatalf("parsed command = %#v", got)
	}
}

func TestParseSlashCommandRejectsInvalidWebCommands(t *testing.T) {
	for _, input := range []string{
		"export",
		"/",
		"/help",
		"/start",
		"/steer",
		"/continue",
		"/rewrite",
		"/importsim",
		"/unknown",
	} {
		t.Run(input, func(t *testing.T) {
			if _, err := parseSlashCommand(input); err == nil {
				t.Fatalf("parseSlashCommand(%q) unexpectedly succeeded", input)
			}
		})
	}
}

func TestParseExportArgsMatchesTUISyntax(t *testing.T) {
	got, err := parseExportArgs([]string{"novel.epub", "from=3", "to=8", "--overwrite"})
	if err != nil {
		t.Fatal(err)
	}
	want := exp.Options{OutPath: "novel.epub", Format: "", From: 3, To: 8, Overwrite: true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("export options = %#v, want %#v", got, want)
	}
}

func TestParseExportArgsRejectsTUIInvalidArguments(t *testing.T) {
	for _, input := range [][]string{
		{"from=-1"},
		{"to=abc"},
		{"format=txt"},
		{"--force"},
		{"one.txt", "two.txt"},
	} {
		t.Run(strings.Join(input, "_"), func(t *testing.T) {
			if _, err := parseExportArgs(input); err == nil {
				t.Fatalf("parseExportArgs(%q) unexpectedly succeeded", input)
			}
		})
	}
}

func TestParseImportArgsMatchesTUISyntax(t *testing.T) {
	got, err := parseImportArgs([]string{"story.md", "from=4"})
	if err != nil {
		t.Fatal(err)
	}
	want := imp.Options{SourcePath: "story.md", ResumeFrom: 4}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("import options = %#v, want %#v", got, want)
	}
}

func TestParseImportArgsRejectsMissingOrUnknownArguments(t *testing.T) {
	for _, input := range [][]string{
		{},
		{"story.md", "from=-1"},
		{"story.md", "to=2"},
		{"story.md", "4"},
	} {
		t.Run(strings.Join(input, "_"), func(t *testing.T) {
			if _, err := parseImportArgs(input); err == nil {
				t.Fatalf("parseImportArgs(%q) unexpectedly succeeded", input)
			}
		})
	}
}

func TestCommandFramesHaveStableProgressAndResultFields(t *testing.T) {
	progress, err := json.Marshal(commandProgressFrame{
		Command: "import",
		Text:    "Đang nhập",
		Stage:   "chapter",
		Current: 2,
		Total:   5,
		Level:   "info",
	})
	if err != nil {
		t.Fatal(err)
	}
	var gotProgress map[string]any
	if err := json.Unmarshal(progress, &gotProgress); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]any{
		"command": "import",
		"text":    "Đang nhập",
		"stage":   "chapter",
		"current": float64(2),
		"total":   float64(5),
		"level":   "info",
	} {
		if gotProgress[key] != want {
			t.Fatalf("progress[%q] = %#v, want %#v", key, gotProgress[key], want)
		}
	}

	result, err := json.Marshal(commandResultFrame{
		Command:     "cocreate",
		Markdown:    "Kế hoạch",
		Prompt:      "Viết tiếp",
		Ready:       true,
		Suggestions: []string{"Gợi ý"},
		Level:       "success",
		Done:        true,
	})
	if err != nil {
		t.Fatal(err)
	}
	var gotResult map[string]any
	if err := json.Unmarshal(result, &gotResult); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]any{
		"command":  "cocreate",
		"markdown": "Kế hoạch",
		"prompt":   "Viết tiếp",
		"ready":    true,
		"level":    "success",
		"done":     true,
	} {
		if gotResult[key] != want {
			t.Fatalf("result[%q] = %#v, want %#v", key, gotResult[key], want)
		}
	}
}
