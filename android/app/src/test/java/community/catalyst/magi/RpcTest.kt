package community.catalyst.magi

import community.catalyst.magi.client.magi.Message
import community.catalyst.magi.client.magi.SayRequest
import community.catalyst.magi.client.magi.SayResponse
import community.catalyst.magi.client.magi.sayRequestFromCbor
import community.catalyst.magi.client.magi.toCbor
import java.io.BufferedInputStream
import java.net.InetAddress
import java.net.ServerSocket
import kotlin.test.AfterTest
import kotlin.test.BeforeTest
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFailsWith

/** A one-thread HTTP/1.1 server: enough for the carrier, without com.sun.net.httpserver. */
private class FakeServer(val handle: (path: String, headers: Map<String, String>, body: ByteArray) -> Triple<Int, String, ByteArray>) {
    val socket = ServerSocket(0, 10, InetAddress.getByName("127.0.0.1"))
    private val thread = Thread {
        while (!socket.isClosed) {
            val s = try { socket.accept() } catch (_: Exception) { return@Thread }
            s.use {
                val input = BufferedInputStream(it.getInputStream())
                fun line(): String {
                    val b = StringBuilder()
                    while (true) {
                        val c = input.read()
                        if (c < 0 || c == '\n'.code) return b.toString().trimEnd('\r')
                        b.append(c.toChar())
                    }
                }
                val path = line().split(" ")[1]
                val headers = generateSequence { line().takeIf { l -> l.isNotEmpty() } }
                    .associate { l -> l.substringBefore(":").lowercase() to l.substringAfter(":").trim() }
                val body = input.readNBytes(headers["content-length"]?.toInt() ?: 0)
                val (status, type, out) = handle(path, headers, body)
                val w = it.getOutputStream()
                w.write("HTTP/1.1 $status X\r\nContent-Type: $type\r\nContent-Length: ${out.size}\r\nConnection: close\r\n\r\n".toByteArray())
                w.write(out)
                w.flush()
            }
        }
    }.apply { isDaemon = true; start() }

    val port get() = socket.localPort
    fun close() = socket.close()
}

class RpcTest {
    private lateinit var server: FakeServer
    private var seenAuth: String? = null
    private var seenAgent: String? = null

    @BeforeTest
    fun start() {
        server = FakeServer { path, headers, body ->
            when (path) {
                "/rpc/MagiService/say" -> {
                    seenAuth = headers["authorization"]
                    seenAgent = headers["user-agent"]
                    val req = sayRequestFromCbor(body)
                    val out = SayResponse(
                        chat = "cht_1",
                        message = Message(
                            id = "m1", chat = "cht_1", seq = 7UL, author = "user:x@example.local", authorKind = "human",
                            modality = if (req.audio != null) "voice" else "text", text = req.text ?: "", refs = emptyList(), createdMs = 1UL,
                        ),
                    ).toCbor()
                    Triple(200, "application/cbor", out)
                }
                else -> Triple(401, "application/json", """{"code":401,"message":"token \"expired\""}""".toByteArray())
            }
        }
    }

    @AfterTest
    fun stop() = server.close()

    private fun base() = "http://127.0.0.1:${server.port}"

    @Test
    fun callRoundTripsThroughTheGeneratedCodec() {
        val c = HttpCarrier(base()) { "tok" }
        val r = c.magi.say(SayRequest(text = "hello", audio = byteArrayOf(1, 2, 3), audioType = "audio/ogg"))
        assertEquals("cht_1", r.chat)
        assertEquals("hello", r.message.text)
        assertEquals("voice", r.message.modality)
        assertEquals(7UL, r.message.seq)
        assertEquals("Bearer tok", seenAuth)
        assertEquals("magi-android/1", seenAgent)
    }

    @Test
    fun errorsCarryTheRpcCode() {
        val c = HttpCarrier(base()) { null }
        val e = assertFailsWith<RpcException> {
            c.magi.read(community.catalyst.magi.client.magi.ReadRequest(chat = "cht_1"))
        }
        assertEquals(401, e.code)
        assertEquals("token \"expired\"", e.message)
    }

    @Test
    fun parseErrorFallsBackToTheBody() {
        assertEquals(502 to "error code: 1010", HttpCarrier.parseError(502, "error code: 1010\n"))
        assertEquals(400 to "bad", HttpCarrier.parseError(500, """{"code": 400, "message": "bad"}"""))
    }
}
