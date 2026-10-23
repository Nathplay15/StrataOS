package io.github.nathplay15.strataos.upload

import android.content.Context
import android.content.pm.ServiceInfo
import android.os.Build
import androidx.work.Constraints
import androidx.work.CoroutineWorker
import androidx.work.ForegroundInfo
import androidx.work.NetworkType
import androidx.work.OneTimeWorkRequestBuilder
import androidx.work.WorkManager
import androidx.work.WorkerParameters
import androidx.work.workDataOf
import io.github.nathplay15.strataos.Graph
import io.github.nathplay15.strataos.R
import io.github.nathplay15.strataos.net.ApiException
import io.github.nathplay15.strataos.net.CreateUpload
import io.github.nathplay15.strataos.notify.Notifier
import java.io.File
import java.io.IOException
import java.io.InputStream
import java.io.RandomAccessFile
import java.nio.channels.Channels
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext

/**
 * Sends files the user chose (Share to StrataOS, Upload in the file browser).
 * They were copied to the app's storage first: the permission to read a
 * shared file ends with the activity that received it, an upload may take
 * longer. Each copy is removed once on the NAS.
 */
class UploadWorker(ctx: Context, params: WorkerParameters) : CoroutineWorker(ctx, params) {
    override suspend fun doWork(): Result = withContext(Dispatchers.IO) {
        val api = Graph.api() ?: return@withContext Result.failure()
        val folder = inputData.getString("folder") ?: return@withContext Result.failure()
        val dir = File(inputData.getString("dir") ?: return@withContext Result.failure())
        val files = dir.listFiles()?.filter { it.isFile && !it.name.startsWith(".") }?.sortedBy { it.name } ?: emptyList()
        val uploader = ChunkedUploader(ApiTransport(api), Graph.backupDb)
        val title = applicationContext.getString(R.string.uploading)
        for ((i, f) in files.withIndex()) {
            val n = Notifier.progress(applicationContext, Notifier.UPLOADS, title, f.name, i, files.size)
            runCatching {
                setForeground(if (Build.VERSION.SDK_INT >= 29) ForegroundInfo(Notifier.BACKUP_ID + 1, n, ServiceInfo.FOREGROUND_SERVICE_TYPE_DATA_SYNC) else ForegroundInfo(Notifier.BACKUP_ID + 1, n))
            }
            try {
                uploader.upload("share:" + f.absolutePath, CreateUpload(folder, f.name, f.length(), f.lastModified()), FileSource(f))
                f.delete()
            } catch (e: ApiException) {
                if (e.status >= 500 || e.status == 429) return@withContext Result.retry()
                f.delete() // refused (rights, quota, name): retrying cannot help
                setProgress(workDataOf("error" to (e.message ?: "")))
            } catch (e: IOException) {
                return@withContext Result.retry()
            }
        }
        dir.delete()
        Result.success()
    }

    companion object {
        fun enqueue(ctx: Context, folder: String, dir: File) {
            WorkManager.getInstance(ctx).enqueue(
                OneTimeWorkRequestBuilder<UploadWorker>()
                    .setConstraints(Constraints.Builder().setRequiredNetworkType(NetworkType.CONNECTED).build())
                    .setInputData(workDataOf("folder" to folder, "dir" to dir.absolutePath))
                    .addTag("upload")
                    .build(),
            )
        }
    }
}

class FileSource(private val f: File) : ChunkSource {
    override val size: Long = f.length()
    override fun openAt(offset: Long): InputStream {
        val raf = RandomAccessFile(f, "r")
        raf.seek(offset)
        return Channels.newInputStream(raf.channel)
    }
}
