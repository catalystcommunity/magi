package community.catalyst.magi

import java.util.Calendar
import java.util.Locale
import java.util.TimeZone
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
    fun otherDayIsOfTheLocalCalendar() {
        assertFalse(ChatTime.otherDay(at(23, 0, 1), at(23, 23, 59)))
        assertTrue(ChatTime.otherDay(at(22, 23, 59), at(23, 0, 1)))
    }

    @Test
    fun builtIsInTheGivenZone() {
        val tokyo = TimeZone.getTimeZone("Asia/Tokyo")
        val ny = TimeZone.getTimeZone("America/New_York")
        val us = Locale.US
        val tokyoResult = ChatTime.built(3_627_275L, tokyo, us).replace('\u202F', ' ')
        val nyResult = ChatTime.built(3_627_275L, ny, us).replace('\u202F', ' ')
        assertEquals("Nov 24, 2026 at 7:35 AM (local time)", tokyoResult)
        assertEquals("Nov 23, 2026 at 5:35 PM (local time)", nyResult)
    }

    @Test
    fun builtUsesTheDefaultZone() {
        val old = TimeZone.getDefault()
        try {
            TimeZone.setDefault(TimeZone.getTimeZone("Asia/Tokyo"))
            val result = ChatTime.built(3_627_275L, locale = Locale.US).replace('\u202F', ' ')
            assertEquals("Nov 24, 2026 at 7:35 AM (local time)", result)
        } finally {
            TimeZone.setDefault(old)
        }
    }

    @Test
    fun builtIsReadable() {
        val utc = TimeZone.getTimeZone("UTC")
        val us = Locale.US
        val result = ChatTime.built(3_627_275L, utc, us).replace('\u202F', ' ')
        assertEquals("Nov 23, 2026 at 10:35 PM (local time)", result)
    }

    @Test
    fun builtKeepsTheLocale() {
        val ny = TimeZone.getTimeZone("America/New_York")
        val de = Locale.GERMANY
        val result = ChatTime.built(3_627_275L, ny, de)
        assertTrue(result.contains("17:35"), result)
        assertFalse(result.contains("PM"), result)
    }
}
