package webhost

import (
	"errors"
	"fmt"
	"strings"
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
	Command  string `json:"command"`
	Markdown string `json:"markdown"`
	Level    string `json:"level"`
	Done     bool   `json:"done"`
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
