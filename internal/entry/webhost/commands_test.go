package webhost

import (
	"reflect"
	"testing"
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
	for _, item := range got {
		expected, ok := want[item.Name]
		if !ok {
			t.Fatalf("unexpected web command /%s", item.Name)
		}
		if item.Usage != expected.usage || item.Description != expected.description {
			t.Fatalf("/%s = usage %q, description %q; want usage %q, description %q", item.Name, item.Usage, item.Description, expected.usage, expected.description)
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
