package model

import "time"

const ParserSchemaVersion = "3"

type FileKind string

const (
	KindPDF   FileKind = "pdf"
	KindDOCX  FileKind = "docx"
	KindXLSX  FileKind = "xlsx"
	KindCSV   FileKind = "csv"
	KindText  FileKind = "text"
	KindMD    FileKind = "markdown"
	KindImage FileKind = "image"
)

type Source struct {
	Path string
	Kind FileKind
	Size int64
}

type Block struct {
	ID    string            `json:"id"`
	Type  string            `json:"type"`
	Text  string            `json:"text"`
	Page  int               `json:"page"`
	Sheet string            `json:"sheet,omitempty"`
	Row   int               `json:"row,omitempty"`
	Ref   string            `json:"ref,omitempty"`
	Meta  map[string]string `json:"meta,omitempty"`
}

type Cell struct {
	Value string `json:"value"`
	Type  string `json:"type,omitempty"`
}

type MediaMeta struct {
	ID      string            `json:"id"`
	MIME    string            `json:"mime"`
	Ext     string            `json:"ext"`
	Width   int               `json:"width,omitempty"`
	Height  int               `json:"height,omitempty"`
	Size    int64             `json:"size"`
	Page    int               `json:"page,omitempty"`
	BlockID string            `json:"blockId,omitempty"`
	OCRText string            `json:"ocrText,omitempty"`
	Meta    map[string]string `json:"meta,omitempty"`
}

type SheetMeta struct {
	Name string `json:"name"`
	Rows int    `json:"rows"`
	Cols int    `json:"cols"`
	File string `json:"file"`
}

type Capabilities struct {
	Text   bool   `json:"text"`
	Tables string `json:"tables"`
	Images bool   `json:"images"`
	OCR    string `json:"ocr"`
}

type Metadata struct {
	DocumentID    string       `json:"documentId"`
	Type          FileKind     `json:"type"`
	SourceName    string       `json:"sourceName"`
	SourcePath    string       `json:"sourcePath"`
	ContentSHA256 string       `json:"contentSha256"`
	ParserSchema  string       `json:"parserSchemaVersion"`
	Size          int64        `json:"size"`
	Pages         int          `json:"pages"`
	PageKind      string       `json:"pageKind"`
	Blocks        int          `json:"blocks"`
	HasTable      bool         `json:"hasTable"`
	HasImage      bool         `json:"hasImage"`
	Sheets        []SheetMeta  `json:"sheets"`
	Images        []MediaMeta  `json:"images"`
	Capabilities  Capabilities `json:"capabilities"`
	OCRWarnings   []string     `json:"ocrWarnings,omitempty"`
	CreatedAt     time.Time    `json:"createdAt"`
}

type Summary struct {
	Pages        int
	PageKind     string
	HasTable     bool
	HasImage     bool
	Capabilities Capabilities
}

type DocumentRef struct {
	Kind           string    `json:"kind"`
	DocumentID     string    `json:"documentId"`
	ObjectPath     string    `json:"objectPath"`
	ParsedArtifact string    `json:"parsedArtifact"`
	SourceArtifact string    `json:"sourceArtifact"`
	Name           string    `json:"name,omitempty"`
	CreatedAt      time.Time `json:"createdAt"`
}
