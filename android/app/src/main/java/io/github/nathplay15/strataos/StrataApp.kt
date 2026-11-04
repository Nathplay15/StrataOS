package io.github.nathplay15.strataos

import android.app.Application
import io.github.nathplay15.strataos.backup.BackupScheduler
import io.github.nathplay15.strataos.notify.EventsWorker
import io.github.nathplay15.strataos.notify.Notifier
import io.github.nathplay15.strataos.sync.SyncScheduler
import io.github.nathplay15.strataos.update.UpdateScheduler

class StrataApp : Application() {
    override fun onCreate() {
        super.onCreate()
        Graph.init(this)
        Notifier.createChannels(this)
        UpdateScheduler.apply(this)
        if (Graph.prefs.server != null) {
            EventsWorker.schedule(this)
            BackupScheduler.apply(this)
            SyncScheduler.apply(this)
        }
    }
}
