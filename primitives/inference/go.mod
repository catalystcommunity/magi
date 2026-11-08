module github.com/catalystcommunity/magi/primitives/inference

go 1.26.0

require (
	github.com/catalystcommunity/magi/lib/authn v0.0.0
	github.com/catalystcommunity/magi/lib/revconn v0.0.0
	github.com/catalystcommunity/magi/lib/rpc v0.0.0
)

require golang.org/x/sys v0.49.0

replace (
	github.com/catalystcommunity/magi/lib/authn => ../../lib/authn
	github.com/catalystcommunity/magi/lib/rpc => ../../lib/rpc
)

replace github.com/catalystcommunity/magi/lib/revconn => ../../lib/revconn

require github.com/catalystcommunity/magi/primitives/identity v0.0.0

replace github.com/catalystcommunity/magi/primitives/identity => ../identity
