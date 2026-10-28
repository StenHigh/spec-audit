//go:build darwin || linux

package main

import (
	"encoding/json"
	"errors"
)

type PublicationFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Bytes  int    `json:"bytes"`
}
type PublicationSnapshot struct {
	SchemaVersion int               `json:"schema_version"`
	ID            string            `json:"id"`
	Files         []PublicationFile `json:"files"`
}

func publicationID(p PublicationSnapshot) string {
	p.ID = ""
	data, _ := json.Marshal(p)
	return digest(data)
}

func validatePublication(p PublicationSnapshot) error {
	if p.SchemaVersion != 1 || p.ID != publicationID(p) || !validDigest(p.ID) || len(p.Files) > 200000 {
		return errors.New("publication: неверная схема/основание")
	}
	seen := map[string]bool{}
	for _, file := range p.Files {
		if !localPath(file.Path) || seen[file.Path] || !validDigest(file.SHA256) || file.Bytes < 0 || file.Bytes > maxFile {
			return errors.New("publication: неверный файл")
		}
		seen[file.Path] = true
	}
	return nil
}
