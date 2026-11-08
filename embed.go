// Package devboxen holds the files the CLI deploys to the hosts: the Ansible plays, the box
// image, the host side and the browser UI's source. They are embedded, so each version of
// the CLI deploys exactly its own files.
package devboxen

import "embed"

//go:embed ansible box host web cmd/devboxen-web go.mod go.sum
var Source embed.FS
