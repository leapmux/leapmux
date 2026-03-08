package id

import (
	"fmt"

	gonanoid "github.com/matoous/go-nanoid/v2"
)

// Generate returns a 48-character nanoid using an alphanumeric alphabet (A-Za-z0-9).
func Generate() string {
	id, err := gonanoid.Generate("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789", 48)
	if err != nil {
		panic(fmt.Sprintf("generate nanoid: %v", err))
	}
	return id
}
