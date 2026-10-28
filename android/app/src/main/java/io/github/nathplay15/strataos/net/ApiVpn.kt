package io.github.nathplay15.strataos.net

import kotlinx.serialization.Serializable
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.buildJsonObject

// VPN: the WireGuard server of the NAS, its devices, and the outgoing tunnel
// (backend api/h_vpn.go, h_vpn_wg.go, h_vpn_client.go). There is no OpenVPN.

@Serializable data class VpnPeer(
    val id: String = "", val name: String = "", val mode: String = "", val ip: String = "", val publicKey: String = "",
    val disabled: Boolean = false, val createdAt: String = "", val owner: String = "", val kind: String = "",
    val services: List<String> = emptyList(), val expires: String = "", val expired: Boolean = false,
    val subnets: List<String> = emptyList(), val endpoint: String = "", val keepalive: Int = 0,
) {
    val site get() = kind == "site"
}

@Serializable data class VpnConfig(
    val enabled: Boolean = false, val port: Int = 51820, val subnet: String = "", val endpoint: String = "", val lan: String = "",
    val publicKey: String = "", val peers: List<VpnPeer> = emptyList(), val defaultMode: String = "", val userDevices: Boolean = false,
    val expiryWarnDays: Int = 0,
)

@Serializable data class PeerStatus(val handshake: Long = 0, val endpoint: String = "", val rx: Long = 0, val tx: Long = 0)
@Serializable data class VpnUserRef(val username: String = "", val disabled: Boolean = false)

@Serializable data class VpnServer(
    val installed: Boolean = false, val config: VpnConfig = VpnConfig(), val serverIp: String = "",
    val status: Map<String, PeerStatus> = emptyMap(), val inactive: Map<String, String> = emptyMap(),
    val users: List<VpnUserRef> = emptyList(), val lanGuess: String = "", val ddnsHost: String = "",
)

@Serializable data class VpnConfigFile(val config: String = "", val name: String = "")
@Serializable data class NewPeer(val peer: VpnPeer = VpnPeer(), val config: String = "")

@Serializable data class VpnClientConfig(val enabled: Boolean = false, val name: String = "", val endpoint: String = "", val scope: String = "", val apps: List<String> = emptyList(), val addresses: List<String> = emptyList())
@Serializable data class VpnClient(
    val installed: Boolean = false, val config: VpnClientConfig = VpnClientConfig(), val status: PeerStatus? = null,
    val routing: Boolean = false, val docker: Boolean = false,
)
@Serializable data class PublicIp(val publicIp: String = "")

@Serializable data class MyVpn(
    val allowed: Boolean = false, val devices: List<VpnPeer> = emptyList(), val status: Map<String, PeerStatus> = emptyMap(),
    val inactive: Map<String, String> = emptyMap(), val max: Int = 0, val mode: String = "",
)

@Serializable data class VpnServerReq(
    val enabled: Boolean, val port: Int, val subnet: String, val endpoint: String, val lan: String,
    val defaultMode: String, val userDevices: Boolean, val expiryWarnDays: Int,
)

/** Add (owner "" = the administrators) or change a device: the access policy goes with it. */
@Serializable data class PeerReq(
    val name: String, val owner: String = "", val kind: String = "", val disabled: Boolean = false, val mode: String = "",
    val services: List<String> = emptyList(), val expires: String = "", val subnets: List<String> = emptyList(),
    val endpoint: String = "", val keepalive: Int = 0,
)

@Serializable private data class NameReq(val name: String)

fun Api.vpn(): VpnServer = call(get("/api/vpn"), VpnServer.serializer())
fun Api.saveVpn(r: VpnServerReq) = call(sendJson("PUT", "/api/vpn", r), okSer)
fun Api.addPeer(r: PeerReq): NewPeer = call(sendJson("POST", "/api/vpn/peers", r), NewPeer.serializer())
fun Api.savePeer(id: String, r: PeerReq) = call(sendJson("PUT", "/api/vpn/peers/$id", r), okSer)
fun Api.peerConfig(id: String): VpnConfigFile = call(get("/api/vpn/peers/$id/config"), VpnConfigFile.serializer())
fun Api.deletePeer(id: String) = ok("DELETE", "/api/vpn/peers/$id")

/** The same peer with its policy unchanged, enabled or disabled. */
fun VpnPeer.toReq(disabled: Boolean = this.disabled) = PeerReq(
    name = name, owner = owner, kind = kind, disabled = disabled, mode = mode, services = services, expires = expires,
    subnets = subnets, endpoint = endpoint, keepalive = keepalive,
)

fun Api.vpnClient(): VpnClient = call(get("/api/vpn/client"), VpnClient.serializer())

/**
 * Turns the outgoing tunnel on or off. The NAS takes the whole client
 * configuration: the current one is sent back as it came (its keys stay
 * masked, the NAS keeps the stored ones when no new file is given).
 */
fun Api.setVpnClient(enabled: Boolean) {
    val raw = call(get("/api/vpn/client"), JsonObject.serializer())
    val cfg = raw["config"] as? JsonObject ?: JsonObject(emptyMap())
    val body = buildJsonObject {
        cfg.forEach { (k, v) -> if (k != "enabled") put(k, v) }
        put("enabled", JsonPrimitive(enabled))
        put("conf", JsonPrimitive(""))
    }
    call(send("PUT", "/api/vpn/client", body.toString()), okSer)
}

fun Api.vpnClientCheck(): PublicIp = call(send("POST", "/api/vpn/client/check"), PublicIp.serializer())
fun Api.vpnClientReconnect() = ok("POST", "/api/vpn/client/reconnect")

fun Api.myVpn(): MyVpn = call(get("/api/vpn/my"), MyVpn.serializer())
fun Api.addMyVpnDevice(name: String): NewPeer = call(sendJson("POST", "/api/vpn/my/devices", NameReq(name)), NewPeer.serializer())
fun Api.myVpnConfig(id: String): VpnConfigFile = call(get("/api/vpn/my/devices/$id/config"), VpnConfigFile.serializer())
fun Api.deleteMyVpnDevice(id: String) = ok("DELETE", "/api/vpn/my/devices/$id")
