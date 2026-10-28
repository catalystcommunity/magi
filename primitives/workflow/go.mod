module github.com/catalystcommunity/magi/primitives/workflow

go 1.26.0

require (
	github.com/CatalystCommunity/corndogs/clients/corndogs v0.0.0-20260930033320-f1b6ec6086dd
	github.com/catalystcommunity/magi/lib/authn v0.0.0
	github.com/catalystcommunity/magi/lib/rpc v0.0.0
	github.com/jackc/pgx/v5 v5.10.0
)

require (
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	golang.org/x/sync v0.17.0 // indirect
	golang.org/x/text v0.29.0 // indirect
)

replace (
	github.com/catalystcommunity/magi/lib/authn => ../../lib/authn
	github.com/catalystcommunity/magi/lib/rpc => ../../lib/rpc
)
