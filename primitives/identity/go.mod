module github.com/catalystcommunity/magi/primitives/identity

go 1.26.4

require (
	github.com/catalystcommunity/linkkeys/sdks/regular-rp/go v0.0.0-20261012235123-03fcbd8e7007
	github.com/catalystcommunity/magi/lib/authn v0.0.0
	github.com/catalystcommunity/magi/lib/rpc v0.0.0
	github.com/jackc/pgx/v5 v5.10.0
)

require (
	github.com/catalystcommunity/csilgen/transports/go v0.0.0-20260713013116-a661c8727022 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	golang.org/x/crypto v0.54.0 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.40.0 // indirect
)

replace (
	github.com/catalystcommunity/magi/lib/authn => ../../lib/authn
	github.com/catalystcommunity/magi/lib/rpc => ../../lib/rpc
)
