package io.github.nathplay15.strataos.ui.control

import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.material3.Button
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.AnnotatedString
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.text.buildAnnotatedString
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.withStyle
import androidx.compose.ui.unit.dp
import io.github.nathplay15.strataos.R
import io.github.nathplay15.strataos.net.manage.b
import io.github.nathplay15.strataos.net.manage.json
import io.github.nathplay15.strataos.net.manage.l
import io.github.nathplay15.strataos.net.manage.o
import io.github.nathplay15.strataos.net.manage.s
import io.github.nathplay15.strataos.net.manage.sendObj
import io.github.nathplay15.strataos.ui.adminkit.AreaScreen
import io.github.nathplay15.strataos.ui.adminkit.Choice
import io.github.nathplay15.strataos.ui.adminkit.Field
import io.github.nathplay15.strataos.ui.adminkit.Refreshable
import io.github.nathplay15.strataos.ui.adminkit.ScrollColumn
import io.github.nathplay15.strataos.ui.adminkit.SectionCard
import io.github.nathplay15.strataos.ui.adminkit.SwitchLine
import io.github.nathplay15.strataos.ui.adminkit.rememberActions
import io.github.nathplay15.strataos.ui.adminkit.rememberData
import kotlinx.serialization.json.JsonObject

// Settings the web keeps in PUT /api/settings (the whole object, secrets
// masked in answers and kept by the NAS when sent back masked): the
// assistant's endpoint and the notification channels. And the user guide.

/** Saves /api/settings with [fields] replaced, the rest as the NAS gave it. */
private fun saveSettings(cur: JsonObject, vararg fields: Pair<String, Any?>) = cur.put(*fields)

@Composable
fun AssistantSettingsScreen(onBack: () -> Unit) {
    val data = rememberData(Unit) { it.json("/api/settings").o() }
    val actions = rememberActions()
    val r = LocalResources.current
    AreaScreen(stringResource(R.string.set_assistant), onBack) { pad ->
        Refreshable(data, pad) { s ->
            var url by remember(s) { mutableStateOf(s.s("assistantUrl")) }
            var key by remember(s) { mutableStateOf(s.s("assistantKey")) }
            var model by remember(s) { mutableStateOf(s.s("assistantModel")) }
            var light by remember(s) { mutableStateOf(s.s("assistantLight")) }
            var think by remember(s) { mutableStateOf(s.b("assistantThink")) }
            val other = s.s("assistantProvider").isNotEmpty()
            ScrollColumn {
                SectionCard {
                    if (other) Hint(stringResource(R.string.set_assistant_provider_web))
                    Field(stringResource(R.string.set_endpoint), url, { url = it.trim() }, help = stringResource(R.string.set_endpoint_help))
                    Field(stringResource(R.string.set_api_key), key, { key = it }, secret = true, help = stringResource(R.string.set_api_key_help))
                    Field(stringResource(R.string.set_model), model, { model = it.trim() }, help = "mistral-small-latest, qwen3:4b…")
                    Choice(stringResource(R.string.set_light), listOf("" to r.getString(R.string.nas_auto), "on" to r.getString(R.string.usr_on), "off" to r.getString(R.string.usr_off)), light, { light = it })
                    Hint(stringResource(R.string.set_light_help))
                    SwitchLine(stringResource(R.string.set_think), think, sub = stringResource(R.string.set_think_help)) { think = it }
                    Hint(stringResource(R.string.set_assistant_help))
                    Button(enabled = !actions.busy, onClick = {
                        actions.run(done = r.getString(R.string.b_saved), after = data.reload) { a ->
                            a.sendObj("PUT", "/api/settings", saveSettings(s, "assistantUrl" to url, "assistantKey" to key, "assistantModel" to model, "assistantLight" to light, "assistantThink" to think))
                        }
                    }) { Text(stringResource(R.string.b_save)) }
                }
            }
        }
    }
}

@Composable
fun ChannelsScreen(onBack: () -> Unit) {
    val data = rememberData(Unit) { it.json("/api/settings").o() }
    val actions = rememberActions()
    val r = LocalResources.current
    AreaScreen(stringResource(R.string.set_channels), onBack) { pad ->
        Refreshable(data, pad) { s ->
            val f = remember(s) { mutableMapOf<String, androidx.compose.runtime.MutableState<String>>() }
            @Composable
            fun field(k: String, label: Int, secret: Boolean = false, number: Boolean = false, help: String? = null) {
                val st = f.getOrPut(k) { mutableStateOf(if (number) s.l(k).toString() else s.s(k)) }
                Field(stringResource(label), st.value, { st.value = if (number) it.filter(Char::isDigit) else it }, secret = secret, number = number, help = help)
            }
            var enc by remember(s) { mutableStateOf(s.s("smtpSecurity").ifEmpty { "auto" }) }
            ScrollColumn {
                SectionCard(stringResource(R.string.nas_ch_email)) {
                    field("smtpHost", R.string.set_smtp_host)
                    field("smtpPort", R.string.ctl_port, number = true)
                    Choice(stringResource(R.string.set_smtp_tls), listOf("auto" to r.getString(R.string.nas_auto), "tls" to "TLS", "starttls" to "STARTTLS", "none" to r.getString(R.string.net_none)), enc, { enc = it })
                    field("smtpUser", R.string.set_smtp_user)
                    field("smtpPassword", R.string.usr_password, secret = true)
                    field("smtpFrom", R.string.set_smtp_from)
                    field("notifyEmail", R.string.set_notify_email)
                }
                SectionCard("Telegram") {
                    field("telegramToken", R.string.set_bot_token, secret = true)
                    field("telegramChatId", R.string.set_chat_id)
                }
                SectionCard("ntfy") {
                    field("ntfyUrl", R.string.set_ntfy_url, help = stringResource(R.string.set_ntfy_help))
                    field("ntfyToken", R.string.set_ntfy_token, secret = true)
                }
                SectionCard("Webhook") { field("webhookUrl", R.string.set_webhook, help = stringResource(R.string.set_webhook_help)) }
                Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                    Button(enabled = !actions.busy, onClick = {
                        val pairs = f.map { (k, v) -> k to (if (k == "smtpPort") (v.value.toIntOrNull() ?: 0) else v.value) }.toMutableList<Pair<String, Any?>>()
                        pairs += "smtpSecurity" to enc
                        actions.run(done = r.getString(R.string.b_saved), after = data.reload) { a -> a.sendObj("PUT", "/api/settings", saveSettings(s, *pairs.toTypedArray())) }
                    }) { Text(stringResource(R.string.b_save)) }
                    OutlinedButton(enabled = !actions.busy, onClick = {
                        actions.run { a -> actions.say(r.getString(R.string.nas_test_sent, a.sendObj("POST", "/api/settings/test-notification").o()["sent"].a2())) }
                    }) { Text(stringResource(R.string.nas_test)) }
                }
            }
        }
    }
}

private fun kotlinx.serialization.json.JsonElement?.a2(): String = (this as? kotlinx.serialization.json.JsonArray)?.joinToString(", ") { it.toString().trim('"') }.orEmpty()

// ---- Help: the user guide (docs/GUIDE*.md, packed in the app's assets) ----

private class GuideSection(val title: String, val lines: List<String>)

private fun loadGuide(ctx: android.content.Context): List<GuideSection> {
    val fr = java.util.Locale.getDefault().language == "fr"
    val text = runCatching { ctx.assets.open(if (fr) "guide/GUIDE.fr.md" else "guide/GUIDE.md").bufferedReader().readText() }.getOrDefault("")
    val out = mutableListOf<GuideSection>()
    var title = ""
    var lines = mutableListOf<String>()
    for (raw in text.lines()) {
        val line = raw.trimEnd()
        if (line.startsWith("## ")) {
            if (title.isNotEmpty() || lines.any { it.isNotBlank() }) out += GuideSection(title, lines)
            title = line.removePrefix("## "); lines = mutableListOf()
        } else if (!line.startsWith("# ") && !line.startsWith("![")) lines += line
    }
    if (title.isNotEmpty()) out += GuideSection(title, lines)
    return out.filter { it.title.isNotEmpty() }
}

/** Inline Markdown of the guide: **bold**, `code`, [text](url) shown as its text. */
private fun inline(t: String): AnnotatedString = buildAnnotatedString {
    val re = Regex("""(\*\*[^*]+\*\*|`[^`]+`|\[[^\]]+]\([^)]+\))""")
    var last = 0
    for (m in re.findAll(t)) {
        append(t.substring(last, m.range.first))
        val v = m.value
        when {
            v.startsWith("**") -> withStyle(SpanStyle(fontWeight = FontWeight.SemiBold)) { append(v.substring(2, v.length - 2)) }
            v.startsWith("`") -> withStyle(SpanStyle(fontFamily = FontFamily.Monospace)) { append(v.substring(1, v.length - 1)) }
            else -> append(v.substring(1, v.indexOf(']')))
        }
        last = m.range.last + 1
    }
    append(t.substring(last))
}

@Composable
fun HelpScreen(onBack: () -> Unit) {
    val ctx = LocalContext.current
    val guide = remember { loadGuide(ctx) }
    var open by remember { mutableStateOf<GuideSection?>(null) }
    val sec = open
    AreaScreen(sec?.title ?: stringResource(R.string.ctl_w_help), if (sec != null) ({ open = null }) else onBack) { pad ->
        Column(Modifier.padding(pad)) {
            ScrollColumn {
                if (sec == null) {
                    if (guide.isEmpty()) Hint(stringResource(R.string.ctl_no_logs))
                    guide.forEach { g -> SectionCard(modifier = Modifier.clickable { open = g }) { Text(g.title, style = MaterialTheme.typography.titleSmall) } }
                } else SectionCard {
                    sec.lines.forEach { l ->
                        val item = Regex("""^\s*(-|\d+\.)\s+(.*)$""").find(l)
                        when {
                            l.isBlank() -> {}
                            l.startsWith("### ") -> Text(inline(l.removePrefix("### ")), style = MaterialTheme.typography.titleSmall, modifier = Modifier.padding(top = 8.dp))
                            item != null -> Row(Modifier.fillMaxWidth()) {
                                Text(if (item.groupValues[1] == "-") "•" else item.groupValues[1], Modifier.padding(end = 8.dp), style = MaterialTheme.typography.bodyMedium)
                                Text(inline(item.groupValues[2]), style = MaterialTheme.typography.bodyMedium)
                            }
                            else -> Text(inline(l), style = MaterialTheme.typography.bodyMedium)
                        }
                    }
                }
            }
        }
    }
}
