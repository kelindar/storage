package storage

// Link is a derived link index entry.
type Link struct {
	Source URN      `json:"source"`
	Target URN      `json:"target"`
	Path   Path     `json:"path"`
	Kind   LinkKind `json:"kind"`
}

// Linker is implemented by documents that declare explicit links.
type Linker interface {
	Links() ([]Link, error)
}

// LinkKind distinguishes exclusive ownership from an ordinary dependency.
type LinkKind uint8

const (
	LinkOwnership LinkKind = iota + 1
	LinkDependency
)

// Own returns an ownership link.
func Own(source, target URN, path Path) Link {
	return Link{Source: source, Target: target, Path: path, Kind: LinkOwnership}
}

// Use returns a dependency link.
func Use(source, target URN, path Path) Link {
	return Link{Source: source, Target: target, Path: path, Kind: LinkDependency}
}
