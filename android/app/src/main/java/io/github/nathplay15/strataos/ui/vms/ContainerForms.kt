package io.github.nathplay15.strataos.ui.vms

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Delete
import androidx.compose.material3.Checkbox
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.unit.dp
import io.github.nathplay15.strataos.R
import io.github.nathplay15.strataos.net.manage.LxcFeatures
import io.github.nathplay15.strataos.net.manage.LxcMount
import io.github.nathplay15.strataos.net.manage.LxcNetwork
import io.github.nathplay15.strataos.net.manage.mountTarget
import io.github.nathplay15.strataos.net.manage.shares
import io.github.nathplay15.strataos.net.manage.validCidr
import io.github.nathplay15.strataos.ui.adminkit.Choice
import io.github.nathplay15.strataos.ui.adminkit.Field
import io.github.nathplay15.strataos.ui.adminkit.SwitchLine
import io.github.nathplay15.strataos.ui.adminkit.rememberData

// Forms shared by the container wizard and the container detail screen.

/** Network of a container: NAT, a bridge of the NAS, or none; static address or DHCP. */
@Composable
fun NetworkEditor(n: LxcNetwork, bridges: List<String>, set: (LxcNetwork) -> Unit) {
    var dns by remember { mutableStateOf(n.dns.joinToString(" ")) }
    val opts = listOf("nat" to stringResource(R.string.ct_net_nat)) + bridges.map { "bridge:$it" to stringResource(R.string.ct_net_bridge, it) } +
        listOf("none" to stringResource(R.string.ct_net_none))
    val cur = if (n.mode == "bridge") "bridge:${n.bridge}" else n.mode
    Column(verticalArrangement = Arrangement.spacedBy(8.dp)) {
        Choice(stringResource(R.string.ct_network), opts + listOfNotNull((cur to cur).takeIf { opts.none { o -> o.first == cur } }), cur, { v ->
            set(if (v.startsWith("bridge:")) n.copy(mode = "bridge", bridge = v.removePrefix("bridge:")) else n.copy(mode = v, bridge = ""))
        })
        if (n.mode != "none") {
            Field(stringResource(R.string.ct_address), n.address, { set(n.copy(address = it.trim())) }, help = stringResource(R.string.ct_address_help),
                error = if (!validCidr(n.address)) stringResource(R.string.ct_address_help) else null)
            if (n.address.isNotEmpty()) {
                Field(stringResource(R.string.ct_gateway), n.gateway, { set(n.copy(gateway = it.trim())) })
                Field(stringResource(R.string.ct_dns), dns, { dns = it; set(n.copy(dns = it.split(Regex("[\\s,]+")).filter(String::isNotEmpty))) })
            }
            Field(stringResource(R.string.ct_mac), n.mac, { set(n.copy(mac = it.trim())) }, help = stringResource(R.string.ct_mac_help))
        }
        if (bridges.isEmpty() && n.mode != "none") Text(stringResource(R.string.ct_no_bridge), style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
    }
}

/** Shared folders mounted into the container. */
@Composable
fun MountsEditor(mounts: List<LxcMount>, privileged: Boolean, mapped: List<String>, set: (List<LxcMount>) -> Unit) {
    val shares = rememberData(Unit) { it.shares() }
    var add by remember { mutableStateOf("") }
    Column(verticalArrangement = Arrangement.spacedBy(8.dp)) {
        if (mounts.isEmpty()) Text(stringResource(R.string.ct_no_mounts), style = MaterialTheme.typography.bodySmall)
        mounts.forEachIndexed { i, m ->
            Column {
                Row(verticalAlignment = Alignment.CenterVertically) {
                    Text(m.share, Modifier.weight(1f), style = MaterialTheme.typography.bodyLarge)
                    Checkbox(m.readOnly, { ro -> set(mounts.mapIndexed { j, x -> if (j == i) x.copy(readOnly = ro) else x }) })
                    Text(stringResource(R.string.ct_read_only), style = MaterialTheme.typography.bodySmall)
                    IconButton(onClick = { set(mounts.filterIndexed { j, _ -> j != i }) }) { Icon(Icons.Default.Delete, stringResource(R.string.b_delete)) }
                }
                Field(stringResource(R.string.ct_mount_point), m.target, { t -> set(mounts.mapIndexed { j, x -> if (j == i) x.copy(target = t) else x }) })
            }
        }
        val free = shares.value.orEmpty().filter { s -> mounts.none { it.share == s.name } }
        if (free.isNotEmpty()) Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
            Choice(stringResource(R.string.ct_add_share), listOf("" to stringResource(R.string.ct_choose)) + free.map { it.name to it.name }, add, { add = it }, Modifier.weight(1f))
            OutlinedButton(enabled = add.isNotEmpty(), onClick = { set(mounts + LxcMount(share = add, target = mountTarget(add))); add = "" }) { Text(stringResource(R.string.ct_add)) }
        }
        Text(stringResource(if (privileged) R.string.ct_mounts_privileged else R.string.ct_mounts_unprivileged), style = MaterialTheme.typography.bodySmall,
            color = MaterialTheme.colorScheme.onSurfaceVariant)
        if (mapped.isNotEmpty()) Text(stringResource(R.string.ct_mapped, mapped.joinToString(", ")), style = MaterialTheme.typography.bodySmall)
    }
}

@Composable
fun FeaturesEditor(f: LxcFeatures, privileged: Boolean, set: (LxcFeatures) -> Unit) {
    Column {
        SwitchLine(stringResource(R.string.ct_nesting), f.nesting || !privileged, enabled = privileged,
            sub = stringResource(if (privileged) R.string.ct_nesting_help else R.string.ct_nesting_unpriv)) { set(f.copy(nesting = it)) }
        SwitchLine("keyctl", f.keyctl, enabled = !privileged, sub = stringResource(R.string.ct_keyctl_help)) { set(f.copy(keyctl = it)) }
        SwitchLine("FUSE", f.fuse, sub = stringResource(R.string.ct_fuse_help)) { set(f.copy(fuse = it)) }
        SwitchLine("TUN", f.tun, sub = stringResource(R.string.ct_tun_help)) { set(f.copy(tun = it)) }
    }
}

@Composable
fun backendLabel(b: String) = stringResource(when (b) {
    "btrfs" -> R.string.ct_backend_btrfs
    "zfs" -> R.string.ct_backend_zfs
    "dir" -> R.string.ct_backend_dir
    else -> R.string.ct_backend_image
})
