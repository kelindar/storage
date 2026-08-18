package main

import (
	"testing"

	"github.com/kelindar/storage"
)

func TestTypes(t *testing.T) {
	registry := newRegistry()
	value := newAutomation("test")
	if value.Workflow.Version != 1 {
		t.Fatalf("workflow version = %d", value.Workflow.Version)
	}
	target, err := storage.New[*namespaceObject]("acme", "default")
	if err != nil {
		t.Fatal(err)
	}
	group := &bundle{Meta: storage.Meta{Tenant: "acme", Namespace: "default", Kind: "bundle", ID: "aaaaaaaaaaaaaaaaaaaa"}, Resources: []storage.URN{target.URN()}}
	links, err := group.Links()
	if err != nil || len(links) != 1 || links[0].Kind != storage.LinkOwnership {
		t.Fatalf("bundle links = %#v, %v", links, err)
	}
	if _, err := registry.Resolve(kindAutomation); err != nil {
		t.Fatal(err)
	}
}
