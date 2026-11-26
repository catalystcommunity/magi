module github.com/catalystcommunity/magi/primitives/identity

go 1.26.4

require (
	github.com/catalystcommunity/linkkeys/sdks/regular-rp/go v0.0.0-20261125235542-0996efcfa718
	github.com/catalystcommunity/magi/lib/authn v0.0.0
	github.com/catalystcommunity/magi/lib/rpc v0.0.0
	github.com/jackc/pgx/v5 v5.10.0
)

require (
	github.com/catalystcommunity/csilgen/transports/go v0.0.0-20260926220402-baec1dad191c // indirect
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
