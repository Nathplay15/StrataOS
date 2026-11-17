package io.github.nathplay15.strataos.net

import kotlinx.serialization.Serializable
import kotlinx.serialization.builtins.ListSerializer
import kotlinx.serialization.json.JsonObject

/** The conversational assistant of the NAS (api/h_assistant.go). */
@Serializable data class AssistantConfig(val enabled: Boolean = false, val model: String = "")
@Serializable data class AssistantConversation(val id: String = "", val title: String = "", val updated: String = "")
@Serializable data class AssistantMsg(val role: String = "", val content: String = "")
@Serializable data class AssistantChat(val id: String = "", val title: String = "", val messages: List<AssistantMsg> = emptyList(), val updated: String = "")
@Serializable data class AssistantAction(val token: String = "", val tool: String = "", val summary: String = "")
@Serializable data class AssistantReply(val reply: String = "", val conversationId: String = "", val actions: List<AssistantAction> = emptyList())
@Serializable private data class AssistantChatReq(val messages: List<AssistantMsg>, val conversationId: String = "")
@Serializable private data class AssistantConfirmReq(val token: String)
@Serializable data class AssistantConfirmResult(val result: String = "")

fun Api.assistantConfig(): AssistantConfig = call(get("/api/assistant/config"), AssistantConfig.serializer())

fun Api.assistantConversations(): List<AssistantConversation> =
    call(get("/api/assistant/conversations"), ListSerializer(AssistantConversation.serializer()))

fun Api.assistantConversation(id: String): AssistantChat = call(get("/api/assistant/conversations/$id"), AssistantChat.serializer())

fun Api.deleteAssistantConversation(id: String) =
    call(request(url("/api/assistant/conversations/$id")).delete().build(), JsonObject.serializer())

fun Api.assistantChat(messages: List<AssistantMsg>, conversationId: String): AssistantReply =
    call(request(url("/api/assistant/chat")).post(body(AssistantChatReq(messages, conversationId))).build(), AssistantReply.serializer())

fun Api.assistantConfirm(token: String): AssistantConfirmResult =
    call(request(url("/api/assistant/confirm")).post(body(AssistantConfirmReq(token))).build(), AssistantConfirmResult.serializer())
