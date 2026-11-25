package community.catalyst.magi

import android.content.Context
import android.content.res.ColorStateList
import android.content.res.Configuration
import android.graphics.Typeface
import android.graphics.drawable.Drawable
import android.graphics.drawable.GradientDrawable
import android.graphics.drawable.RippleDrawable
import android.text.InputType
import android.view.Gravity
import android.view.View
import android.view.ViewGroup.LayoutParams.MATCH_PARENT
import android.view.ViewGroup.LayoutParams.WRAP_CONTENT
import android.widget.EditText
import android.widget.ImageView
import android.widget.LinearLayout
import android.widget.TextView
import java.text.DateFormat
import java.util.Calendar
import java.util.Date
import java.util.Locale
import java.util.TimeZone

/** The colors of a theme. The dark theme is the default (brand/colors.css). */
class Palette(val dark: Boolean) {
    val bg = if (dark) 0xFF0C1524.toInt() else 0xFFF7F5EF.toInt()
    /** Bars and cards. */
    val surface = if (dark) 0xFF142235.toInt() else 0xFFFFFFFF.toInt()
    /** Fields and the bubbles of other authors. */
    val raised = if (dark) 0xFF1E3049.toInt() else 0xFFE9E6DC.toInt()
    val text = if (dark) 0xFFEEF2F7.toInt() else 0xFF142D4E.toInt()
    val muted = if (dark) 0xFF93A3B8.toInt() else 0xFF5C6B80.toInt()
    val line = if (dark) 0xFF263A55.toInt() else 0xFFD9D5C8.toInt()
    /** The yellow of the star: the one action of a page, the tab in use. */
    val accent = if (dark) 0xFFFFD34E.toInt() else 0xFF142D4E.toInt()
    val onAccent = if (dark) 0xFF142D4E.toInt() else 0xFFFFFFFF.toInt()
    /** The bubble of the person who uses the app. */
    val mine = if (dark) 0xFF2A5C9E.toInt() else 0xFF142D4E.toInt()
    val onMine = 0xFFFFFFFF.toInt()
    val onMineMuted = 0xB3FFFFFF.toInt()
    val danger = if (dark) 0xFFFF8A80.toInt() else 0xFFC92A2A.toInt()
    val notice = if (dark) 0xFF3A3116.toInt() else 0xFFFFF1C2.toInt()

    companion object {
        /** theme is "dark", "light", or "system" (Settings.theme). */
        fun dark(theme: String, context: Context): Boolean = when (theme) {
            "light" -> false
            "system" -> context.resources.configuration.uiMode and Configuration.UI_MODE_NIGHT_MASK != Configuration.UI_MODE_NIGHT_NO
            else -> true
        }
    }
}

/** A web link in a message. A mark of the sentence at its end is not a part of it (the same rule as the speech of Magi). */
val LINK = Regex("""https?://[^\s<>"]+[^\s<>".,;:!?)\]]""")

/** What a bubble shows for a link: all of a short link, the host and "/…" of a long one. */
fun linkLabel(url: String): String {
    if (url.length <= 48) return url
    val host = url.substringAfter("://").substringBefore('/').substringBefore('?')
    return "$host/\u2026"
}

/** The words of a chat for a time: the clock time in a bubble, the day between bubbles. */
object ChatTime {
    fun clock(ms: Long): String = DateFormat.getTimeInstance(DateFormat.SHORT).format(Date(ms))

    /** "Today", "Yesterday", or the date. now is a parameter for the tests. */
    fun day(ms: Long, now: Long = System.currentTimeMillis()): String {
        val days = dayNumber(now) - dayNumber(ms)
        return when (days) {
            0L -> "Today"
            1L -> "Yesterday"
            else -> DateFormat.getDateInstance(DateFormat.MEDIUM).format(Date(ms))
        }
    }

    /**
     * The time of a build from its version code. The code is the minute of
     * the build since 2020 (tools/build-android.sh).
     */
    fun buildMs(versionCode: Long): Long = (versionCode * 60 + 1_577_836_800L) * 1000

    /**
     * The build time as a readable string in the given zone and locale.
     * zone and locale are parameters for the tests, as day does for now.
     */
    fun built(versionCode: Long, zone: TimeZone = TimeZone.getDefault(), locale: Locale = Locale.getDefault()): String {
        val ms = buildMs(versionCode)
        val dateFmt = DateFormat.getDateInstance(DateFormat.MEDIUM, locale).apply { timeZone = zone }
        val timeFmt = DateFormat.getTimeInstance(DateFormat.SHORT, locale).apply { timeZone = zone }
        return "${dateFmt.format(Date(ms))} at ${timeFmt.format(Date(ms))} (local time)"
    }

    /** True when two times are on different days of the local calendar. */
    fun otherDay(a: Long, b: Long): Boolean = dayNumber(a) != dayNumber(b)

    /** The count of local days since the epoch. */
    private fun dayNumber(ms: Long): Long {
        val c = Calendar.getInstance().apply { timeInMillis = ms }
        return (ms + c.get(Calendar.ZONE_OFFSET) + c.get(Calendar.DST_OFFSET)).floorDiv(86_400_000L)
    }
}

/** The widgets of the app in the colors of a palette. Sizes are in dp. */
class Ui(private val c: Context, val p: Palette) {
    private val density = c.resources.displayMetrics.density

    fun dp(v: Int): Int = (v * density + 0.5f).toInt()

    fun shape(color: Int, radius: Int, stroke: Int? = null): GradientDrawable = GradientDrawable().apply {
        setColor(color)
        cornerRadius = dp(radius).toFloat()
        if (stroke != null) setStroke(dp(1), stroke)
    }

    /** A bubble: the corner next to its author is smaller, as in other chat apps. */
    fun bubble(color: Int, mine: Boolean): GradientDrawable = GradientDrawable().apply {
        setColor(color)
        val r = dp(18).toFloat()
        val s = dp(5).toFloat()
        // top-left, top-right, bottom-right, bottom-left
        cornerRadii = if (mine) floatArrayOf(r, r, r, r, s, s, r, r) else floatArrayOf(r, r, r, r, r, r, s, s)
    }

    private fun pressable(d: Drawable): Drawable =
        RippleDrawable(ColorStateList.valueOf(if (p.dark) 0x33FFFFFF else 0x22000000), d, null)

    fun text(s: CharSequence, size: Float = 16f, color: Int = p.text, bold: Boolean = false): TextView = TextView(c).apply {
        text = s
        textSize = size
        setTextColor(color)
        setLinkTextColor(p.accent)
        if (bold) setTypeface(typeface, Typeface.BOLD)
    }

    /**
     * The text of a message with its web links as links. A long link shows
     * as its host and "/…", so that a link with a long token does not fill
     * the bubble. Text with no link comes back as it is.
     */
    fun linked(text: String): CharSequence {
        val found = LINK.findAll(text).toList()
        if (found.isEmpty()) return text
        val out = android.text.SpannableStringBuilder()
        var last = 0
        for (m in found) {
            out.append(text, last, m.range.first)
            val url = m.value
            val start = out.length
            out.append(linkLabel(url))
            out.setSpan(android.text.style.URLSpan(url), start, out.length, android.text.Spanned.SPAN_EXCLUSIVE_EXCLUSIVE)
            last = m.range.last + 1
        }
        out.append(text, last, text.length)
        return out
    }

    enum class Kind { PRIMARY, TONAL, DANGER, LINK }

    /** A button. PRIMARY is the one action of a page. */
    fun button(label: String, kind: Kind = Kind.TONAL, onClick: () -> Unit): TextView {
        val (bg, fg) = when (kind) {
            Kind.PRIMARY -> p.accent to p.onAccent
            Kind.TONAL -> p.raised to p.text
            Kind.DANGER -> p.raised to p.danger
            Kind.LINK -> 0 to p.accent
        }
        return text(label, 14f, fg, bold = true).apply {
            gravity = Gravity.CENTER
            minHeight = dp(40)
            setPadding(dp(16), dp(8), dp(16), dp(8))
            background = pressable(shape(bg, 20))
            isClickable = true
            isFocusable = true
            setOnClickListener { onClick() }
        }
    }

    /** A round button with an icon. desc is the name for a screen reader. */
    fun icon(res: Int, desc: String, tint: Int = p.text, bg: Int = 0, size: Int = 44, onClick: () -> Unit): ImageView = ImageView(c).apply {
        setImageResource(res)
        imageTintList = ColorStateList.valueOf(tint)
        contentDescription = desc
        val pad = dp(size) / 4
        setPadding(pad, pad, pad, pad)
        background = pressable(shape(bg, size / 2))
        layoutParams = LinearLayout.LayoutParams(dp(size), dp(size))
        isClickable = true
        isFocusable = true
        setOnClickListener { onClick() }
    }

    fun field(hint: String, value: String = "", type: Int = 0, lines: Boolean = false): EditText = EditText(c).apply {
        this.hint = hint
        setText(value)
        textSize = 16f
        setTextColor(p.text)
        setHintTextColor(p.muted)
        inputType = InputType.TYPE_CLASS_TEXT or type or
            (if (lines) InputType.TYPE_TEXT_FLAG_MULTI_LINE or InputType.TYPE_TEXT_FLAG_CAP_SENTENCES else 0)
        if (lines) maxLines = 6 else isSingleLine = true
        background = shape(p.raised, 22)
        setPadding(dp(16), dp(10), dp(16), dp(10))
        minHeight = dp(44)
    }

    /** A chip of a row of choices. */
    fun chip(label: String, selected: Boolean, onClick: () -> Unit): TextView =
        text(label, 14f, if (selected) p.onAccent else p.text, bold = selected).apply {
            gravity = Gravity.CENTER
            setPadding(dp(14), dp(6), dp(14), dp(6))
            background = pressable(shape(if (selected) p.accent else p.raised, 16))
            isClickable = true
            setOnClickListener { onClick() }
        }

    fun column(pad: Int = 0): LinearLayout = LinearLayout(c).apply {
        orientation = LinearLayout.VERTICAL
        setPadding(dp(pad), dp(pad), dp(pad), dp(pad))
    }

    fun row(): LinearLayout = LinearLayout(c).apply {
        orientation = LinearLayout.HORIZONTAL
        gravity = Gravity.CENTER_VERTICAL
    }

    /** A card of a list page. */
    fun card(vararg views: View): LinearLayout = column(14).apply {
        background = shape(p.surface, 16)
        views.forEach { addView(it) }
        layoutParams = LinearLayout.LayoutParams(MATCH_PARENT, WRAP_CONTENT).apply { bottomMargin = dp(10) }
    }

    /** Empty space between two views of a column or a row. */
    fun gap(size: Int): View = View(c).apply { layoutParams = LinearLayout.LayoutParams(dp(size), dp(size)) }

    /** A circle with the first letter of a name. */
    fun avatar(name: String, size: Int = 36): TextView = text(name.trim().take(1).uppercase(), size * 0.42f, p.onAccent, bold = true).apply {
        gravity = Gravity.CENTER
        background = shape(p.accent, size / 2)
        layoutParams = LinearLayout.LayoutParams(dp(size), dp(size))
    }
}
