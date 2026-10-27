package io.github.nathplay15.strataos.ui.player

import android.app.PendingIntent
import android.content.Intent
import androidx.media3.session.MediaSession
import androidx.media3.session.MediaSessionService

/**
 * Keeps playback going with the screen off or in another app (music), with
 * the media notification and lock screen controls. The player screen binds
 * to it while open; it stops itself once nothing plays.
 */
class PlaybackService : MediaSessionService() {
    private var session: MediaSession? = null

    override fun onCreate() {
        super.onCreate()
        val open = PendingIntent.getActivity(this, 0, Intent(this, PlayerActivity::class.java).addFlags(Intent.FLAG_ACTIVITY_SINGLE_TOP),
            PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT)
        session = MediaSession.Builder(this, Playback.ensurePlayer(this)).setSessionActivity(open).build()
    }

    override fun onGetSession(controllerInfo: MediaSession.ControllerInfo): MediaSession? {
        // A new player since this service started (the previous one was closed).
        session?.let { s -> Playback.ensurePlayer(this).let { p -> if (s.player !== p) s.player = p } }
        return session
    }

    override fun onTaskRemoved(rootIntent: Intent?) {
        val p = session?.player
        if (p == null || !p.playWhenReady || p.mediaItemCount == 0) stopSelf()
    }

    override fun onDestroy() {
        session?.release()
        session = null
        // The player itself stays: the player screen may still show it; it is
        // released when that screen closes.
        super.onDestroy()
    }
}
