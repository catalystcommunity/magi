package community.catalyst.magi

import community.catalyst.magi.client.identity.IdentityClient
import community.catalyst.magi.client.magi.MagiClient
import java.io.IOException
import java.net.HttpURLConnection
import java.net.URL

/** An error that the server returned: an RPC code (HTTP status) and a message. */
class RpcException(val code: Int, message: String) : IOException(message)

/**
 * Carrier for the generated clients. A call is POST {base}/rpc/{service}/{op}
 * with a CBOR body and the bearer token. An error is a non-2xx status with a
 * JSON body {"code": int, "message": str}.
 */
class HttpCarrier(base: String, private val token: () -> String?) {
    private val base = base.trimEnd('/')

    fun call(service: String, op: String, body: ByteArray): ByteArray {
        val c = URL("$base/rpc/$service/$op").openConnection() as HttpURLConnection
        try {
            c.requestMethod = "POST"
            c.doOutput = true
            c.connectTimeout = 15_000
            c.readTimeout = 120_000 // long polls wait up to 30 s; a reply can take longer
            c.setRequestProperty("Content-Type", "application/cbor")
            c.setRequestProperty("User-Agent", "magi-android/1")
            token()?.let { c.setRequestProperty("Authorization", "Bearer $it") }
            c.outputStream.use { it.write(body) }
            val status = c.responseCode
            if (status in 200..299) return c.inputStream.use { it.readBytes() }
            val text = (c.errorStream?.use { it.readBytes() } ?: ByteArray(0)).toString(Charsets.UTF_8)
            val (code, message) = parseError(status, text)
            throw RpcException(code, message.ifEmpty { "HTTP $status" })
        } finally {
            c.disconnect()
        }
    }

    companion object {
        private val codeRE = Regex(""""code"\s*:\s*(\d+)""")
        private val messageRE = Regex(""""message"\s*:\s*"((?:[^"\\]|\\.)*)"""")

        /** Reads {"code", "message"} without a JSON library; other bodies become the message. */
        fun parseError(status: Int, body: String): Pair<Int, String> {
            val code = codeRE.find(body)?.groupValues?.get(1)?.toIntOrNull()
            val message = messageRE.find(body)?.groupValues?.get(1)
                ?.replace("""\"""", "\"")?.replace("""\\""", "\\")
            return if (code == null && message == null) status to body.trim().take(300) else (code ?: status) to (message ?: "")
        }
    }

    val identity = IdentityClient(object : community.catalyst.magi.client.identity.Transport {
        override fun call(service: String, op: String, request: ByteArray) = this@HttpCarrier.call(service, op, request)
    })

    val magi = MagiClient(object : community.catalyst.magi.client.magi.Transport {
        override fun call(service: String, op: String, request: ByteArray) = this@HttpCarrier.call(service, op, request)
    })
}
