# AI Novel web chat UI redesign

## Mục tiêu

Đưa web host về đúng mô hình sử dụng của TUI: một ô chat duy nhất để gửi yêu
cầu tự do hoặc lệnh `/`, phản hồi từ server hiển thị như hội thoại và dữ liệu
Markdown được trình bày như nội dung đọc được. Web không tự đặt thêm các khái
niệm `Start`, `Steer`, `Continue`; selector chỉ là cách chọn nhanh lệnh TUI.

## Phạm vi lệnh

Web hiển thị và thực thi đúng các lệnh được yêu cầu:

| Lệnh | Hành vi web |
|---|---|
| `/model [vai-trò]` | Mở bảng chọn model; selector gửi đúng vai trò, provider và model được chọn. |
| `/diag` | Chạy chẩn đoán hiện có, trả báo cáo Markdown và đường dẫn bản export ẩn danh. |
| `/export` | Dùng parser/options cùng ngữ nghĩa TUI để xuất TXT hoặc EPUB theo đường dẫn/tham số. |
| `/import <đường-dẫn>` | Chạy import hiện có, đưa tiến độ từng bước vào luồng chat. |
| `/simulate` | Chạy simulation hiện có, đưa tiến độ từng bước vào luồng chat. |
| `/cocreate` | Mở đồng sáng tác theo giai đoạn; phản hồi streaming và bản nháp đi qua chat để người dùng tiếp tục hoặc kết thúc theo flow hiện có. |

`/help` không xuất hiện trong web. Phím tắt được thể hiện bằng hướng dẫn trong
composer và các nút/panel tương ứng. Danh sách selector không bổ sung lệnh
không có trong bảng trên.

## Dữ liệu trạng thái

`host.UISnapshot` đã chứa dữ liệu cần thiết cho TUI và đang được trả nguyên vẹn
trong `GET /status`. Không tạo một nguồn trạng thái mới. Web sẽ dùng các nhóm
trường sau:

- Tổng quan luôn hiện: `RuntimeState`, `StatusLabel`, `Phase`, `Flow`,
  `Provider`, `ModelName`, `ModelContextWindow`, `NovelName`, chương và số từ.
- Panel chi tiết: `Agents`, `Context*`, `Total*Tokens`, chi phí, ngân sách,
  cache, `PendingRewrites`, `PendingSteer`, `RecoveryLabel` và các thông tin
  chi tiết liên quan.

Frontend gọi lại `/status` theo chu kỳ 3 giây để tương đương nhịp snapshot của
TUI. SSE vẫn là kênh thời gian thực cho stream delta, host event, command result,
câu hỏi và terminal; event không thay thế snapshot đầy đủ.

## Luồng giao diện

### Hội thoại

- Yêu cầu người dùng là tin nhắn ngắn căn phải.
- Phản hồi AI là vùng toàn chiều rộng, không nằm trong bubble; nội dung dài có
  thể cuộn theo trang.
- `stream_clear` bắt đầu một phản hồi assistant mới; `stream_delta` nối vào
  phản hồi hiện tại; `terminal` đóng trạng thái đang stream.
- `host_event` và kết quả lệnh hiển thị như các mục hệ thống/assistant có nhãn
  agent, loại sự kiện và trạng thái, để log vẫn đọc được như một cuộc hội thoại.

### Composer và selector

- Enter gửi nội dung.
- Khi nội dung bắt đầu bằng `/`, selector lọc theo tên/mô tả tiếng Việt và chèn
  đúng usage TUI.
- `/model` mở selector model thay vì gửi một lệnh UI tự chế.
- Esc xóa nội dung hoặc đóng panel đang mở.
- Tab có nút/chỉ dẫn đổi chế độ tương ứng với startup mode hiện có; không đổi
  semantics của core.

### Markdown

Renderer phải escape HTML trước khi tạo markup. Hỗ trợ các dạng cần cho output
core/tool: heading, paragraph, emphasis/strong, inline code, link, unordered và
ordered list, blockquote, fenced code block, horizontal rule và bảng đơn giản.
Không cho phép raw HTML từ model/tool đi thẳng vào DOM.

## API và adapter

Giữ `/status`, `/events` và `/commands` làm bề mặt chính. Bổ sung contract tối
thiểu cho:

- danh sách model/role để dựng `/model` selector;
- dispatch command theo cú pháp slash với parser/validation bám theo TUI;
- kết quả lệnh và tiến độ lệnh qua SSE bằng payload có command, text/Markdown,
  level và cờ hoàn thành;
- state cần thiết cho flow đồng sáng tác, import và simulation.

Adapter web gọi các API core hiện có (`Host` và các package `diag`, `exp`, `imp`,
`sim`) thay vì sao chép logic viết truyện. Các lỗi validation được trả về trong
chat bằng tiếng Việt và HTTP response vẫn có mã lỗi phù hợp.

## Kiểm thử và xác minh

- Go: test parser/dispatch command, model endpoint, command result/progress và
  serialization snapshot; giữ regression tests cho lifecycle command hiện có.
- JavaScript: test các hàm parse field, Markdown escaping/rendering, ghép delta
  thành message, xử lý reconnect và cập nhật snapshot.
- Static checks: `gofmt`, `go vet ./internal/entry/webhost`, focused Go tests,
  `git diff --check` và build binary/Docker image.
- Không tuyên bố acceptance browser/Nginx nếu môi trường không có quyền bind
  listener hoặc không có workspace/provider hợp lệ.

## Ngoài phạm vi

- Không đổi core flow, prompt, tên role hoặc semantics của TUI command.
- Không thêm xác thực, database chat riêng hoặc cơ chế stream mới.
- Không thay đổi hệ màu hiện tại của frontend; chỉ tổ chức lại layout và thêm
  style cần thiết cho chat/Markdown/status.
