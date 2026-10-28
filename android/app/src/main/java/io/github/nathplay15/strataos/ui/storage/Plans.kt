package io.github.nathplay15.strataos.ui.storage

import io.github.nathplay15.strataos.net.Disk
import io.github.nathplay15.strataos.net.RaidArray

// Pure rules of the creation flows (unit tested): which devices may be
// used, how many a layout needs, what the result holds.

/** A layout choice: [min] devices, usable capacity from (count, smallest size). */
data class Layout(val id: String, val label: String, val min: Int, val redundant: Boolean, val capacity: (Int, Long) -> Long)

val raidLevels = listOf(
    Layout("1", "RAID 1", 2, true) { _, s -> s },
    Layout("5", "RAID 5", 3, true) { n, s -> (n - 1) * s },
    Layout("6", "RAID 6", 4, true) { n, s -> (n - 2) * s },
    Layout("10", "RAID 10", 2, true) { n, s -> n * s / 2 },
    Layout("0", "RAID 0", 2, false) { n, s -> n * s },
    Layout("linear", "JBOD", 1, false) { n, s -> n * s },
)

val zfsLayouts = listOf(
    Layout("mirror", "Mirror", 2, true) { _, s -> s },
    Layout("raidz1", "RAIDZ1", 3, true) { n, s -> (n - 1) * s },
    Layout("raidz2", "RAIDZ2", 4, true) { n, s -> (n - 2) * s },
    Layout("raidz3", "RAIDZ3", 5, true) { n, s -> (n - 3) * s },
    Layout("stripe", "Stripe", 1, false) { n, s -> n * s },
)

val btrfsProfiles = listOf(
    Layout("single", "Single", 1, false) { n, s -> n * s },
    Layout("raid1", "RAID 1", 2, true) { n, s -> n * s / 2 },
    Layout("raid1c3", "RAID 1C3", 3, true) { n, s -> n * s / 3 },
    Layout("raid10", "RAID 10", 4, true) { n, s -> n * s / 2 },
    Layout("raid0", "RAID 0", 2, false) { n, s -> n * s },
)

/** A device a new volume, array or pool can be built on. */
data class Candidate(val path: String, val title: String, val detail: String, val size: Long, val disk: Boolean)

/** Free whole disks (never the system disk, never one in use), largest first. */
fun freeDisks(disks: List<Disk>): List<Candidate> = disks.filter { it.free }
    .map { Candidate(it.path, it.title, listOf(it.serial, it.kind.uppercase()).filter { s -> s.isNotBlank() }.joinToString(" · "), it.size, true) }
    .sortedByDescending { it.size }

/** Free disks plus RAID arrays that hold no file system yet (a volume may sit on an array). */
fun volumeCandidates(disks: List<Disk>, raids: List<RaidArray>): List<Candidate> =
    freeDisks(disks) + raids.filter { it.fsType.isEmpty() && it.usedBy.isEmpty() && it.health != "failed" && it.health != "inactive" }
        .map { Candidate(it.device, (it.label.ifEmpty { it.name }) + " (RAID " + it.level.removePrefix("raid") + ")", it.health, it.size, false) }

/** Usable capacity of [count] devices of at least [smallest] bytes in layout [l] (0 when not enough). */
fun capacityOf(l: Layout, count: Int, smallest: Long): Long = if (count < l.min) 0 else l.capacity(count, smallest)

private val nameRe = Regex("^[A-Za-z0-9][A-Za-z0-9_-]{0,31}$")

/** Names of volumes, arrays and pools: letters, digits, - and _ (as the NAS checks). */
fun validStorageName(n: String) = nameRe.matches(n)

/** The lines of the confirmation: every device that will be erased. */
fun eraseList(selected: List<Candidate>, size: (Long) -> String): List<String> =
    selected.map { listOf(it.path, it.title, size(it.size), it.detail).filter { s -> s.isNotBlank() }.joinToString(" — ") }
