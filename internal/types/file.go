package types

import "github.com/gotd/td/tg"

type File struct {
	Location tg.InputFileLocationClass
	FileSize int64
	FileName string // raw attribute, may be empty
	MimeType string
	ID       int64
}
