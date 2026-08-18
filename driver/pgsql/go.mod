module github.com/kelindar/storage/driver/pgsql

go 1.25.0

require (
	github.com/jackc/pgx/v5 v5.10.0
	github.com/kelindar/async v1.6.0
	github.com/kelindar/storage v0.0.0
	github.com/rs/xid v1.6.0
	github.com/zeebo/xxh3 v1.0.2
)

require (
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/klauspost/compress v1.19.0 // indirect
	github.com/klauspost/cpuid/v2 v2.0.9 // indirect
	github.com/kr/text v0.2.0 // indirect
	github.com/rogpeppe/go-internal v1.16.0 // indirect
	github.com/tidwall/gjson v1.19.0 // indirect
	github.com/tidwall/match v1.1.1 // indirect
	github.com/tidwall/pretty v1.2.0 // indirect
	github.com/tidwall/sjson v1.2.5 // indirect
	go.yaml.in/yaml/v2 v2.4.2 // indirect
	golang.org/x/sync v0.17.0 // indirect
	golang.org/x/text v0.29.0 // indirect
	sigs.k8s.io/yaml v1.6.0 // indirect
)

replace github.com/kelindar/storage => ../..
