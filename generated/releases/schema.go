package releaseschema

import _ "embed"

//go:embed go/releases.csil-schema.cbor
var Descriptor []byte

const Digest = "ed829dd0abb849b46db8df4ab5b6214a98bf070b04a1d59fa6342d90d7d56bf8"
