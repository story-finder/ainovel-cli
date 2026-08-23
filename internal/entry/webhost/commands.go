package webhost

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/voocel/ainovel-cli/internal/host/exp"
	"github.com/voocel/ainovel-cli/internal/host/imp"
)

type webCommandSpec struct {
	Name        string
	Usage       string
	Description string
}

type slashCommand struct {
	Name string
	Args []string
}

var webModelRoles = []string{"default", "coordinator", "architect", "writer", "editor"}

type modelProviderResponse struct {
	Provider string   `json:"provider"`
	Models   []string `json:"models"`
}

type modelSelectionResponse struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Explicit bool   `json:"explicit"`
}

type modelCatalogResponse struct {
	Role      string                  `json:"role"`
	Roles     []string                `json:"roles"`
	Providers []modelProviderResponse `json:"providers"`
	Current   modelSelectionResponse  `json:"current"`
}

type commandResultFrame struct {
	Command     string   `json:"command"`
	Markdown    string   `json:"markdown"`
	Prompt      string   `json:"prompt,omitempty"`
	Ready       bool     `json:"ready"`
	Suggestions []string `json:"suggestions,omitempty"`
	Error       string   `json:"error,omitempty"`
	Level       string   `json:"level"`
	Done        bool     `json:"done"`
}

type commandProgressFrame struct {
	Command string `json:"command"`
	Text    string `json:"text"`
	Stage   string `json:"stage,omitempty"`
	Current int    `json:"current,omitempty"`
	Total   int    `json:"total,omitempty"`
	Level   string `json:"level"`
}

func normalizeWebModelRole(role string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "", "default":
		return "default", true
	case "coordinator", "architect", "writer", "editor":
		return strings.ToLower(strings.TrimSpace(role)), true
	default:
		return "", false
	}
}

func webModelRoleList() []string {
	return append([]string(nil), webModelRoles...)
}

func commandCatalog() []webCommandSpec {
	return []webCommandSpec{
		{
			Name:        "model",
			Usage:       "/model [vai-trò]",
			Description: "Chuyển đổi mô hình mặc định hoặc theo vai trò",
		},
		{
			Name:        "diag",
			Usage:       "/diag",
			Description: "Chẩn đoán tình trạng sáng tác tiểu thuyết",
		},
		{
			Name:        "export",
			Usage:       "/export",
			Description: "Xuất truyện các chương đã hoàn thành sang TXT/EPUB",
		},
		{
			Name:        "import",
			Usage:       "/import <đường-dẫn>",
			Description: "Nhập truyện bên ngoài để tiếp tục viết",
		},
		{
			Name:        "simulate",
			Usage:       "/simulate",
			Description: "Đọc ./simulate để tạo hoặc cập nhật tăng dần hồ sơ mô phỏng phong cách viết",
		},
		{
			Name:        "cocreate",
			Usage:       "/cocreate",
			Description: "Tạm dừng sáng tác, đồng sáng tác lên kế hoạch cho các giai đoạn tiếp theo",
		},
	}
}

func parseSlashCommand(text string) (slashCommand, error) {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "/") {
		return slashCommand{}, errors.New("lệnh phải bắt đầu bằng dấu /")
	}

	fields := strings.Fields(strings.TrimPrefix(text, "/"))
	if len(fields) == 0 {
		return slashCommand{}, errors.New("tên lệnh không được để trống")
	}

	name := strings.ToLower(fields[0])
	switch name {
	case "help":
		return slashCommand{}, errors.New("lệnh /help không khả dụng trên web")
	case "start", "steer", "continue", "rewrite":
		return slashCommand{}, fmt.Errorf("lệnh /%s không khả dụng trên web", name)
	}

	known := false
	for _, spec := range commandCatalog() {
		if spec.Name == name {
			known = true
			break
		}
	}
	if !known {
		return slashCommand{}, fmt.Errorf("lệnh không xác định: /%s", name)
	}

	return slashCommand{Name: name, Args: fields[1:]}, nil
}

// parseExportArgs mirrors the TUI syntax: at most one positional output path,
// optional from/to ranges, and the overwrite flag. Format remains empty so the
// export core can infer TXT/EPUB from the output extension.
func parseExportArgs(args []string) (exp.Options, error) {
	var opts exp.Options
	for _, arg := range args {
		if arg == "--overwrite" {
			opts.Overwrite = true
			continue
		}
		if key, value, ok := strings.Cut(arg, "="); ok {
			switch strings.ToLower(key) {
			case "from":
				n, err := strconv.Atoi(value)
				if err != nil || n < 0 {
					return exp.Options{}, fmt.Errorf("from phải là số nguyên không âm: %q", value)
				}
				opts.From = n
			case "to":
				n, err := strconv.Atoi(value)
				if err != nil || n < 0 {
					return exp.Options{}, fmt.Errorf("to phải là số nguyên không âm: %q", value)
				}
				opts.To = n
			default:
				return exp.Options{}, fmt.Errorf("tham số không xác định %q (hỗ trợ: from / to)", key)
			}
			continue
		}
		if strings.HasPrefix(arg, "-") {
			return exp.Options{}, fmt.Errorf("flag không xác định %q", arg)
		}
		if opts.OutPath != "" {
			return exp.Options{}, fmt.Errorf("chỉ hỗ trợ một tham số đường dẫn: %q", arg)
		}
		opts.OutPath = arg
	}
	return opts, nil
}

// parseImportArgs mirrors the TUI syntax: one source path followed by an
// optional non-negative from=N selector.
func parseImportArgs(args []string) (imp.Options, error) {
	if len(args) == 0 {
		return imp.Options{}, fmt.Errorf("cách dùng: /import <đường dẫn tệp> [from=N]")
	}
	opts := imp.Options{SourcePath: args[0]}
	for _, arg := range args[1:] {
		key, value, ok := strings.Cut(arg, "=")
		if !ok {
			return imp.Options{}, fmt.Errorf("tham số phải có dạng key=value: %q", arg)
		}
		switch strings.ToLower(key) {
		case "from":
			n, err := strconv.Atoi(value)
			if err != nil || n < 0 {
				return imp.Options{}, fmt.Errorf("from phải là số nguyên không âm: %q", value)
			}
			opts.ResumeFrom = n
		default:
			return imp.Options{}, fmt.Errorf("tham số không xác định %q (hỗ trợ: from)", key)
		}
	}
	return opts, nil
}

func humanBytes(n int) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	default:
		return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
	}
}

func briefIntList(values []int, max int) string {
	if len(values) == 0 {
		return ""
	}
	parts := make([]string, 0, len(values))
	for i, value := range values {
		if i >= max {
			parts = append(parts, "...")
			break
		}
		parts = append(parts, strconv.Itoa(value))
	}
	return strings.Join(parts, ",")
}
