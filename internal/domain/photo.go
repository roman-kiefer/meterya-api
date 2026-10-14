package domain

import "time"

type PhotoCrop struct {
	Left   int `json:"left"`
	Top    int `json:"top"`
	Right  int `json:"right"`
	Bottom int `json:"bottom"`
}
type PhotoEdit struct {
	Rotation int        `json:"rotation,omitempty"`
	Crop     *PhotoCrop `json:"crop,omitempty"`
}

type Photo struct {
	Edits          []PhotoEdit `json:"edits,omitempty"`
	EditedSHA256   string      `json:"edited_sha256,omitempty"`
	EditedSize     int64       `json:"edited_size,omitempty"`
	EditedURL      string      `json:"edited_url,omitempty"`
	ID             string      `json:"id"`
	PointID        string      `json:"point_id,omitempty"`
	ReadingID      string      `json:"reading_id,omitempty"`
	RegisterID     string      `json:"register_id,omitempty"`
	Name           string      `json:"name"`
	MIME           string      `json:"mime"`
	SHA256         string      `json:"sha256"`
	Size           int64       `json:"size"`
	Width          int         `json:"width"`
	Height         int         `json:"height"`
	PreviewWidth   int         `json:"preview_width"`
	PreviewHeight  int         `json:"preview_height"`
	CapturedDate   string      `json:"captured_date,omitempty"`
	UploadedAt     time.Time   `json:"uploaded_at"`
	Actor          string      `json:"actor"`
	ConfirmedDate  string      `json:"confirmed_date,omitempty"`
	ConfirmedValue string      `json:"confirmed_value,omitempty"`
	Status         string      `json:"status,omitempty"`
	PreviewURL     string      `json:"preview_url,omitempty"`
	OriginalURL    string      `json:"original_url,omitempty"`
}
type PhotoProposal struct {
	Photo             Photo    `json:"photo"`
	PointID           string   `json:"point_id,omitempty"`
	RegisterID        string   `json:"register_id,omitempty"`
	Serial            string   `json:"serial,omitempty"`
	Value             string   `json:"value,omitempty"`
	Date              string   `json:"date"`
	ExistingReadingID string   `json:"existing_reading_id,omitempty"`
	ExistingValue     string   `json:"existing_value,omitempty"`
	CanAttach         bool     `json:"can_attach"`
	CanCreate         bool     `json:"can_create"`
	ExpectedVersion   int64    `json:"expected_version,omitempty"`
	Flags             []string `json:"flags"`
}
