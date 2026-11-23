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
    fun otherDayIsOfTheLocalCalendar() {
        assertFalse(ChatTime.otherDay(at(23, 0, 1), at(23, 23, 59)))
        assertTrue(ChatTime.otherDay(at(22, 23, 59), at(23, 0, 1)))
    }
}
