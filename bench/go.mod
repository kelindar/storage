module github.com/kelindar/storage/bench

go 1.25.0

require (
	github.com/kelindar/bench v0.3.2
	github.com/kelindar/storage v0.0.0
	github.com/kelindar/storage/driver/sqlite v0.0.0
	github.com/rs/xid v1.6.0
)

require (
	github.com/kelindar/async v1.6.0 // indirect
	github.com/klauspost/compress v1.19.0 // indirect
	github.com/klauspost/cpuid/v2 v2.0.9 // indirect
	github.com/ncruces/go-sqlite3 v0.35.1 // indirect
	github.com/ncruces/go-sqlite3-wasm/v3 v3.1.35302 // indirect
	github.com/ncruces/julianday v1.0.0 // indirect
	github.com/tidwall/gjson v1.19.0 // indirect
	github.com/tidwall/match v1.1.1 // indirect
	github.com/tidwall/pretty v1.2.0 // indirect
	github.com/tidwall/sjson v1.2.5 // indirect
	github.com/zeebo/xxh3 v1.0.2 // indirect
	go.yaml.in/yaml/v2 v2.4.2 // indirect
	golang.org/x/sys v0.46.0 // indirect
	gonum.org/v1/gonum v0.17.0 // indirect
	sigs.k8s.io/yaml v1.6.0 // indirect
)

replace github.com/kelindar/storage => ../

replace github.com/kelindar/storage/driver/sqlite => ../driver/sqlite
