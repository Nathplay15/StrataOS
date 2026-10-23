package io.github.nathplay15.strataos.ui

import android.content.Intent
import android.net.Uri
import android.os.Bundle
import android.widget.Toast
import androidx.activity.compose.setContent
import androidx.core.content.IntentCompat
import androidx.fragment.app.FragmentActivity
import androidx.lifecycle.lifecycleScope
import io.github.nathplay15.strataos.Graph
import io.github.nathplay15.strataos.R
import io.github.nathplay15.strataos.upload.UploadWorker
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext

/** "Share to StrataOS" from any app: pick a folder, the files are sent in the background. */
class ShareActivity : FragmentActivity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        val uris: List<Uri> = when (intent.action) {
            Intent.ACTION_SEND -> listOfNotNull(IntentCompat.getParcelableExtra(intent, Intent.EXTRA_STREAM, Uri::class.java))
            Intent.ACTION_SEND_MULTIPLE -> IntentCompat.getParcelableArrayListExtra(intent, Intent.EXTRA_STREAM, Uri::class.java) ?: emptyList()
            else -> emptyList()
        }
        if (uris.isEmpty() || Graph.api() == null) {
            Toast.makeText(this, getString(if (uris.isEmpty()) R.string.share_nothing else R.string.share_sign_in_first), Toast.LENGTH_LONG).show()
            finish()
            return
        }
        setContent {
            StrataTheme {
                FolderPicker(getString(R.string.share_to, uris.size), onCancel = { finish() }, onPick = { folder ->
                    lifecycleScope.launch {
                        val dir = withContext(Dispatchers.IO) { stageUris(this@ShareActivity, uris) }
                        UploadWorker.enqueue(this@ShareActivity, folder, dir)
                        Toast.makeText(this@ShareActivity, getString(R.string.upload_started, uris.size), Toast.LENGTH_SHORT).show()
                        finish()
                    }
                })
            }
        }
    }
}
