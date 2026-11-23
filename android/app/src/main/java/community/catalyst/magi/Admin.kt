package community.catalyst.magi

import android.app.AlertDialog
import android.graphics.Typeface
import android.view.ViewGroup.LayoutParams.MATCH_PARENT
import android.view.ViewGroup.LayoutParams.WRAP_CONTENT
import android.widget.LinearLayout
import android.widget.ScrollView
import android.widget.TextView
import community.catalyst.magi.Ui.Kind
import community.catalyst.magi.client.magi.AdminChat
import community.catalyst.magi.client.magi.AdminGetRunOutputRequest
import community.catalyst.magi.client.magi.AdminGetRunRequest
import community.catalyst.magi.client.magi.AdminListChatsRequest
import community.catalyst.magi.client.magi.AdminListRunsRequest
import community.catalyst.magi.client.magi.AdminReadChatRequest
import community.catalyst.magi.client.magi.AdminTurnLogRequest
import community.catalyst.magi.client.magi.ListRunnersRequest
import community.catalyst.magi.client.magi.QueryKnowledgeRequest

// The admin views: what an operator sees (get-access). Magi checks the role
// of the caller for each call again; the tab only shows the views.

private val SECTIONS = listOf("Runs", "Chats", "Knowledge", "Runners")
private val KINDS = listOf("", "code-change", "persona-turn", "todo")
private val STATES = listOf("", "running", "waiting", "succeeded", "failed", "cancelled")

/** A dialog can show this much text and stay fast. */
private const val TEXT_LIMIT = 60_000

private fun who(subject: String) = subject.removePrefix("user:").removePrefix("persona:")

internal fun MainActivity.showAdmin(section: String = "Runs") {
    val chips = ui.row()
    for (s in SECTIONS) {
        if (chips.childCount > 0) chips.addView(ui.gap(8))
        chips.addView(ui.chip(s, s == section) { if (s != section) showAdmin(s) })
    }
    val list = ui.column()
    val status = note("Loading...")
    page("Admin", "Admin", actions(chips), ui.gap(12), status, list)
    when (section) {
        "Chats" -> adminChats(list, status)
        "Knowledge" -> adminKnowledge(list, status)
        "Runners" -> adminRunners(list, status)
        else -> adminRuns(list, status)
    }
}

/** The runs of all people: code changes, persona turns, and to-do items. */
private fun MainActivity.adminRuns(list: LinearLayout, status: TextView) {
    var kind = ""
    var state = ""
    val filters = ui.column()
    val rows = ui.column()
    list.addView(filters)
    list.addView(rows)
    fun load() {
        filters.removeAllViews()
        val groups = listOf<Triple<List<String>, String, (String) -> Unit>>(
            Triple(KINDS, kind, { v -> kind = v }),
            Triple(STATES, state, { v -> state = v }),
        )
        for ((options, current, set) in groups) {
            val row = ui.row()
            for (o in options) {
                if (row.childCount > 0) row.addView(ui.gap(6))
                row.addView(ui.chip(o.ifEmpty { "All" }, o == current) { set(o); load() })
            }
            filters.addView(actions(row))
            filters.addView(ui.gap(8))
        }
        status.text = "Loading..."
        background(status) {
            val runs = client().magi.adminListRuns(AdminListRunsRequest(definition = kind.ifEmpty { null }, status = state.ifEmpty { null }, limit = 100UL)).runs
            runOnUiThread {
                rows.removeAllViews()
                status.text = if (runs.isEmpty()) "No runs." else "${runs.size} runs of all people. The newest is first."
                for (r in runs) {
                    val color = when (r.status) { "waiting" -> ui.p.accent; "failed" -> ui.p.danger; else -> ui.p.muted }
                    val box = ui.card(
                        ui.text(r.title ?: r.definition, 15f, bold = true).apply { maxLines = 2; ellipsize = android.text.TextUtils.TruncateAt.END },
                        ui.text(listOf(r.status, r.currentStep).filter { it.isNotEmpty() }.joinToString(" · "), 13f, color),
                        ui.text("${r.definition} · ${who(r.owner)} · ${day(r.updatedMs)}", 12f, ui.p.muted),
                    )
                    r.error?.let { box.addView(ui.text(it, 12f, ui.p.danger).apply { maxLines = 3 }) }
                    box.isClickable = true
                    box.setOnClickListener { adminRun(r.id) }
                    rows.addView(box)
                }
            }
        }
    }
    load()
}

/** One run: its steps with their notes, its data, and its outputs. */
private fun MainActivity.adminRun(id: String) {
    background(null) {
        val run = runCatching { client().magi.adminGetRun(AdminGetRunRequest(id = id)) }
        runOnUiThread {
            val r = run.getOrNull()
            if (r == null) {
                textDialog("Run", "Magi did not give the run: ${run.exceptionOrNull()?.message}")
                return@runOnUiThread
            }
            val col = ui.column(18)
            col.addView(ui.text(r.summary.title ?: r.summary.definition, 17f, bold = true))
            col.addView(ui.text("${r.summary.status} · ${r.summary.definition} · ${who(r.summary.owner)}", 13f, ui.p.muted))
            col.addView(ui.text("${r.summary.id}\nStarted ${day(r.summary.createdMs)}, changed ${day(r.summary.updatedMs)}", 12f, ui.p.muted).apply { setTextIsSelectable(true) })
            r.summary.error?.let { col.addView(ui.text(it, 13f, ui.p.danger).apply { setPadding(0, ui.dp(6), 0, 0) }) }
            val buttons = ui.row()
            fun output(label: String, read: () -> String) {
                if (buttons.childCount > 0) buttons.addView(ui.gap(8))
                buttons.addView(ui.button(label) {
                    background(null) {
                        val text = runCatching(read).getOrElse { "Magi did not give it: ${it.message}" }
                        runOnUiThread { textDialog(label, text.ifEmpty { "Empty." }) }
                    }
                })
            }
            if (r.summary.definition == "code-change") {
                output("Diff") { client().magi.adminGetRunDiff(AdminGetRunRequest(id = id)).patch }
                output("Check output") { client().magi.adminGetRunOutput(AdminGetRunOutputRequest(id = id, name = "check.output")).text }
                output("Transcript") { client().magi.adminGetRunOutput(AdminGetRunOutputRequest(id = id, name = "apprentice.transcript")).text }
                col.addView(ui.gap(10))
                col.addView(actions(buttons))
            }
            col.addView(section("Steps"))
            for (s in r.steps) {
                col.addView(ui.text(listOfNotNull(s.step, s.status, s.outcome).joinToString(" · "), 14f, bold = true).apply { setPadding(0, ui.dp(8), 0, 0) })
                for (n in s.notes) col.addView(ui.text(n, 12f, ui.p.muted).apply { maxLines = 4; setOnClickListener { maxLines = if (maxLines == 4) 400 else 4 } })
            }
            col.addView(section("Data"))
            for ((k, v) in r.vars.toSortedMap()) {
                col.addView(ui.text(k, 12f, ui.p.accent, bold = true).apply { setPadding(0, ui.dp(8), 0, 0) })
                col.addView(ui.text(v, 12f).apply { typeface = Typeface.MONOSPACE; maxLines = 3; setOnClickListener { maxLines = if (maxLines == 3) 400 else 3 } })
            }
            AlertDialog.Builder(this).setView(ScrollView(this).apply { addView(col) }).setPositiveButton("Close", null).show()
        }
    }
}

/** The conversations of all people with the personas. */
private fun MainActivity.adminChats(list: LinearLayout, status: TextView) {
    background(status) {
        val chats = client().magi.adminListChats(AdminListChatsRequest()).chats.sortedByDescending { it.updatedMs }
        runOnUiThread {
            status.text = if (chats.isEmpty()) "No conversations." else "${chats.size} conversations of all people. Magi logs each read with your name."
            for (c in chats) {
                val box = ui.card(
                    ui.text("${who(c.person)} with ${personaName(c.persona)}", 15f, bold = true),
                    ui.text("${c.lastSeq} messages · last ${day(c.updatedMs)}", 12f, ui.p.muted),
                )
                box.isClickable = true
                box.setOnClickListener { adminChat(c) }
                list.addView(box)
            }
        }
    }
}

/** One conversation, newest messages. A touch on a message of the person shows the turn log of the reply. */
private fun MainActivity.adminChat(c: AdminChat) {
    val list = ui.column()
    val status = note("Loading...")
    page("Admin", "${who(c.person)} with ${personaName(c.persona)}", ui.button("All conversations", Kind.LINK) { showAdmin("Chats") }.apply { gravity = android.view.Gravity.START }, status, list)
    back = { showAdmin("Chats") }
    background(status) {
        val r = client().magi.adminReadChat(AdminReadChatRequest(stream = c.stream, limit = 100UL))
        runOnUiThread {
            status.text = if (r.messages.isEmpty()) "No messages." else "The newest ${r.messages.size} messages. Touch a message of the person for the turn log."
            for (m in r.messages) {
                val human = m.authorKind == "human"
                val box = messageView(m, compact = true, right = human)
                if (human) box.getChildAt(0).setOnClickListener {
                    background(null) {
                        val log = runCatching { client().magi.adminTurnLog(AdminTurnLogRequest(message = m.id)).log }
                        runOnUiThread { textDialog("Turn log", log.getOrElse { "Magi did not give it: ${it.message}" } ?: "No turn log for this message.") }
                    }
                }
                list.addView(box, LinearLayout.LayoutParams(MATCH_PARENT, WRAP_CONTENT).apply { topMargin = ui.dp(6) })
            }
        }
    }
}

/** What the caller knows: the things and the records of the knowledge graph. */
private fun MainActivity.adminKnowledge(list: LinearLayout, status: TextView) {
    val query = ui.field("Find a person, a thing, a fact")
    val rows = ui.column()
    fun load() {
        status.text = "Loading..."
        val text = query.text.toString().trim()
        background(status) {
            val r = client().magi.queryKnowledge(QueryKnowledgeRequest(text = text.ifEmpty { null }, limit = 50UL, includeEnded = true))
            runOnUiThread {
                rows.removeAllViews()
                status.text = if (r.items.isEmpty() && r.records.isEmpty()) "Nothing found."
                    else "Your knowledge graph: ${r.items.size} things and ${r.records.size} records. The API has no view of the graphs of other people yet."
                for (i in r.items) {
                    val box = ui.card(ui.text(i.label, 15f, bold = true), ui.text(listOf(i.kind, i.summary).filter { it.isNotEmpty() }.joinToString(" · "), 13f, ui.p.muted))
                    for (f in i.facts) box.addView(ui.text("${f.relation} ${if (f.direction == "in") "←" else "→"} ${f.other ?: f.value ?: ""}" + if (f.status != "current") " (${f.status})" else "", 13f)
                        .apply { setPadding(0, ui.dp(4), 0, 0) })
                    rows.addView(box)
                }
                for (k in r.records) rows.addView(ui.card(
                    ui.text(k.title, 15f, bold = true),
                    ui.text("${k.type} · ${k.kind} · ${day(k.atMs)}", 12f, ui.p.muted),
                    ui.text(k.summary, 13f).apply { maxLines = 4; setOnClickListener { maxLines = if (maxLines == 4) 400 else 4 } },
                ))
            }
        }
    }
    val row = ui.row().apply {
        addView(query, LinearLayout.LayoutParams(0, WRAP_CONTENT, 1f))
        addView(ui.gap(8))
        addView(ui.button("Find", Kind.PRIMARY) { load() })
    }
    list.addView(row)
    list.addView(ui.gap(12))
    list.addView(rows)
    load()
}

/** The runners that are connected, and the runner release that Magi gives them. */
private fun MainActivity.adminRunners(list: LinearLayout, status: TextView) {
    background(status) {
        val r = client().magi.listRunners(ListRunnersRequest())
        runOnUiThread {
            status.text = (if (r.runners.isEmpty()) "No runner is connected." else "${r.runners.size} runners.") + (r.release?.let { " Runner release $it." } ?: "")
            for (n in r.runners) {
                val box = ui.card(
                    ui.text(n.name, 15f, bold = true),
                    ui.text("${if (n.busy) "Busy" else "Idle"} · ${n.version} · ${n.os}/${n.arch}", 13f, if (n.busy) ui.p.accent else ui.p.muted),
                    ui.text("Seen ${day(n.seenMs)}", 12f, ui.p.muted),
                )
                if (n.repos.isNotEmpty()) box.addView(ui.text("Repositories: " + n.repos.joinToString(", "), 12f, ui.p.muted))
                n.updateError?.let { box.addView(ui.text("Update: $it", 12f, ui.p.danger)) }
                list.addView(box)
            }
        }
    }
}

private fun MainActivity.section(name: String) = ui.text(name, 13f, ui.p.muted, bold = true).apply { setPadding(0, ui.dp(18), 0, 0) }

/** A long text (a diff, a log) in a dialog. A text over the limit shows its end. */
private fun MainActivity.textDialog(title: String, text: String) {
    val cut = if (text.length > TEXT_LIMIT) "[the first ${text.length - TEXT_LIMIT} characters are not shown]\n" + text.takeLast(TEXT_LIMIT) else text
    val view = ui.text(cut, 11f).apply {
        typeface = Typeface.MONOSPACE
        setTextIsSelectable(true)
        setPadding(ui.dp(16), ui.dp(12), ui.dp(16), ui.dp(12))
    }
    AlertDialog.Builder(this).setTitle(title).setView(ScrollView(this).apply { addView(view) }).setPositiveButton("Close", null).show()
}
