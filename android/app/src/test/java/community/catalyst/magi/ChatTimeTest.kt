package community.catalyst.magi

import java.util.Calendar
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFalse
import kotlin.test.assertTrue

class ChatTimeTest {
    private fun at(day: Int, hour: Int, minute: Int = 0): Long =
        Calendar.getInstance().apply { clear(); set(2026, Calendar.NOVEMBER, day, hour, minute) }.timeInMillis

    @Test
    fun dayWords() {
        val now = at(23, 9)
        assertEquals("Today", ChatTime.day(at(23, 0, 1), now))
        assertEquals("Yesterday", ChatTime.day(at(22, 23, 59), now))
        // An earlier day is a date, not a word.
        val earlier = ChatTime.day(at(20, 12), now)
        assertTrue(earlier != "Today" && earlier != "Yesterday" && earlier.contains("20"), earlier)
    }

    @Test
    fun buildTimeFromTheVersionCode() {
        // 2026-11-23 22:35 UTC, the minute of a real build.
        assertEquals(1_795_473_300_000L, ChatTime.buildMs(3_627_275L))
    }

    @Test
    fun linksOfAMessage() {
        val text = "Open https://example.local/auth?token=abc. Then see (https://example.local/a/very/long/path/with/a/token/0123456789abcdef0123456789)."
        val found = LINK.findAll(text).map { it.value }.toList()
        // The mark of the sentence after a link is not in the link.
        assertEquals(listOf("https://example.local/auth?token=abc", "https://example.local/a/very/long/path/with/a/token/0123456789abcdef0123456789"), found)
        assertEquals("https://example.local/auth?token=abc", linkLabel(found[0]))
        assertEquals("example.local/\u2026", linkLabel(found[1]))
    }

    @Test
    fun otherDayIsOfTheLocalCalendar() {
        assertFalse(ChatTime.otherDay(at(23, 0, 1), at(23, 23, 59)))
        assertTrue(ChatTime.otherDay(at(22, 23, 59), at(23, 0, 1)))
    }
}
