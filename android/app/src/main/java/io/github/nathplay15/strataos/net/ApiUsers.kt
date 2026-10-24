package io.github.nathplay15.strataos.net

import kotlinx.serialization.Serializable
import kotlinx.serialization.builtins.ListSerializer
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.jsonPrimitive

// Users, groups, quotas and share permissions (backend api/h_users.go,
// h_quota.go, h_shares.go). Administrators only.

@Serializable data class NasUser(
    val username: String = "", val fullName: String = "", val email: String = "", val role: String = "user",
    val disabled: Boolean = false, val ssh: Boolean = false, val docker: Boolean = false, val groups: List<String> = emptyList(),
    val createdAt: String = "", val twoFactor: Boolean = false, val passkeys: Int = 0, val source: String = "",
) {
    val admin get() = role == "admin"
    /** Directory (Active Directory / LDAP) account: its password and Linux identity belong to the directory. */
    val directory get() = source.isNotEmpty()
}

@Serializable data class NasGroup(val name: String = "", val gid: Int = 0, val members: List<String> = emptyList(), val system: Boolean = false)

@Serializable data class UserCreate(
    val username: String, val password: String, val fullName: String, val email: String, val role: String,
    val groups: List<String>, val ssh: Boolean, val docker: Boolean,
)

@Serializable data class UserUpdate(
    val fullName: String, val email: String, val role: String, val disabled: Boolean, val groups: List<String>,
    val ssh: Boolean, val docker: Boolean, val password: String = "",
)

@Serializable data class UserQuota(
    val kind: String = "", val target: String = "", val name: String = "", val mountPoint: String = "",
    val enabled: Boolean = false, val supported: Boolean = false, val used: Long = 0, val limit: Long = 0,
)

@Serializable private data class QuotaReq(val kind: String, val target: String, val limit: Long)
@Serializable private data class GroupReq(val name: String = "", val members: List<String>)

/** A share as listed; the whole object goes back when saving (see setSharePermission). */
@Serializable data class ShareRef(val name: String = "", val path: String = "", val permissions: Map<String, String> = emptyMap(), val guest: Boolean = false)

fun Api.users(): List<NasUser> = call(get("/api/users"), ListSerializer(NasUser.serializer()))
fun Api.createUser(r: UserCreate) = call(sendJson("POST", "/api/users", r), okSer)
fun Api.updateUser(name: String, r: UserUpdate) = call(sendJson("PUT", "/api/users/$name", r), okSer)
fun Api.deleteUser(name: String, removeHome: Boolean) =
    call(send("DELETE", "/api/users/$name", null, *(if (removeHome) arrayOf("removeHome" to "1") else emptyArray())), okSer)

/** Removes the two-factor authentication of another account (its owner sets it up again). */
fun Api.reset2fa(name: String) = ok("POST", "/api/users/$name/2fa/reset")

fun Api.groups(): List<NasGroup> = call(get("/api/groups"), ListSerializer(NasGroup.serializer()))
fun Api.createGroup(name: String, members: List<String>) = call(sendJson("POST", "/api/groups", GroupReq(name, members)), okSer)
fun Api.setGroupMembers(name: String, members: List<String>) = call(sendJson("PUT", "/api/groups/$name", GroupReq(members = members)), okSer)
fun Api.deleteGroup(name: String) = ok("DELETE", "/api/groups/$name")

fun Api.userQuotas(name: String): List<UserQuota> = call(get("/api/users/$name/quotas"), ListSerializer(UserQuota.serializer()))
fun Api.setUserQuota(name: String, kind: String, target: String, limit: Long) = call(sendJson("PUT", "/api/users/$name/quotas", QuotaReq(kind, target, limit)), okSer)

fun Api.shares(): List<ShareRef> = call(get("/api/shares"), ListSerializer(ShareRef.serializer()))

/**
 * Gives [who] (a user, or "@group") [perm] (rw, ro, none, or "" to remove the
 * entry) on a share. The NAS replaces the whole share: the current one is read
 * and sent back with only its permissions changed.
 */
fun Api.setSharePermission(share: String, who: String, perm: String) {
    val list = call(get("/api/shares"), ListSerializer(JsonObject.serializer()))
    val cur = list.firstOrNull { it["name"]?.jsonPrimitive?.content == share } ?: throw ApiException(404, "share not found")
    val perms = (cur["permissions"] as? JsonObject)?.toMutableMap() ?: mutableMapOf()
    if (perm.isEmpty()) perms.remove(who) else perms[who] = JsonPrimitive(perm)
    val body = buildJsonObject {
        cur.forEach { (k, v) -> if (k != "permissions" && k != "locked") put(k, v) }
        put("permissions", JsonObject(perms))
    }
    call(send("PUT", "/api/shares/$share", body.toString()), okSer)
}

/** The password rules of the NAS (accounts.ValidPassword). */
fun passwordProblem(p: String): PasswordProblem? = when {
    p.toByteArray().size < 8 -> PasswordProblem.TooShort // bytes, like the NAS
    p.any { it == '\n' || it == '\r' || it == ':' } -> PasswordProblem.Forbidden
    else -> null
}

enum class PasswordProblem { TooShort, Forbidden }

/** Linux account names the NAS accepts (lower case, digits, - and _). */
val userNameRe = Regex("^[a-z_][a-z0-9_-]{0,31}$")
