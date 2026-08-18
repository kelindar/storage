package main

import (
	"strconv"

	"github.com/kelindar/storage"
	"github.com/kelindar/storage/state"
)

const (
	kindAutomation     storage.Kind = "automation"
	kindNamespace      storage.Kind = "namespace"
	automationWorkflow              = "workflow"
)

type automation struct {
	storage.Meta `kind:"automation" json:",inline"`
	Name         string   `json:"name"`
	Desc         string   `json:"desc,omitempty"`
	Type         string   `json:"type"`
	Workflow     workflow `json:"workflow"`
}

type workflow struct {
	Version int `json:"version"`
}

type namespaceObject struct {
	storage.Meta `kind:"namespace" json:",inline"`
	Name         string `json:"name"`
}

type bundle struct {
	storage.Meta `kind:"bundle" json:",inline"`
	Name         string        `json:"name"`
	Resources    []storage.URN `json:"resources"`
}

func (b *bundle) Links() ([]storage.Link, error) {
	links := make([]storage.Link, 0, len(b.Resources))
	for i, target := range b.Resources {
		links = append(links, storage.Own(b.URN(), target, storage.Path("resources."+strconv.Itoa(i))))
	}
	return links, nil
}

func newRegistry() storage.Registry {
	registry := storage.NewRegistry()
	storage.MustRegister[*automation](registry)
	storage.MustRegister[*namespaceObject](registry)
	storage.MustRegister[*bundle](registry)
	storage.MustRegister[*storage.Blob](registry, storage.Options{
		States: state.Machine{
			"create": "* -> active",
			"delete": "active -> deleting",
		},
	})
	return registry
}
