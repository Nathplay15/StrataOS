package api

// grafanaDashboard returns a Grafana dashboard for the StrataOS metrics
// (import it, then choose the Prometheus data source).
func grafanaDashboard() map[string]any {
	id := 0
	panel := func(title, typ string, x, y, w, h int, unit string, targets ...[2]string) map[string]any {
		id++
		var t []map[string]any
		for i, q := range targets {
			t = append(t, map[string]any{"refId": string(rune('A' + i)), "expr": q[0], "legendFormat": q[1], "datasource": map[string]string{"type": "prometheus", "uid": "${DS_PROMETHEUS}"}})
		}
		return map[string]any{
			"id": id, "title": title, "type": typ,
			"gridPos":     map[string]int{"x": x, "y": y, "w": w, "h": h},
			"datasource":  map[string]string{"type": "prometheus", "uid": "${DS_PROMETHEUS}"},
			"fieldConfig": map[string]any{"defaults": map[string]any{"unit": unit}, "overrides": []any{}},
			"options":     map[string]any{},
			"targets":     t,
		}
	}
	host := `{instance=~"$instance"}`
	panels := []any{
		panel("Processeur", "timeseries", 0, 0, 8, 7, "percent", [2]string{`100 * (1 - rate(strata_cpu_seconds_total{mode="idle",instance=~"$instance"}[5m]) / ignoring(mode) sum without(mode) (rate(strata_cpu_seconds_total` + host + `[5m])))`, "CPU"}),
		panel("Mémoire utilisée", "timeseries", 8, 0, 8, 7, "bytes", [2]string{`strata_memory_total_bytes` + host + ` - strata_memory_available_bytes` + host, "utilisée"}, [2]string{`strata_memory_total_bytes` + host, "totale"}),
		panel("Alertes actives", "stat", 16, 0, 8, 7, "none", [2]string{`strata_alerts` + host, "{{level}}"}),
		panel("Réseau", "timeseries", 0, 7, 12, 8, "Bps", [2]string{`rate(strata_network_receive_bytes_total` + host + `[5m])`, "reçu {{interface}}"}, [2]string{`rate(strata_network_transmit_bytes_total` + host + `[5m])`, "envoyé {{interface}}"}),
		panel("Disques (E/S)", "timeseries", 12, 7, 12, 8, "Bps", [2]string{`rate(strata_disk_read_bytes_total` + host + `[5m])`, "lu {{device}}"}, [2]string{`rate(strata_disk_written_bytes_total` + host + `[5m])`, "écrit {{device}}"}),
		panel("Remplissage des volumes", "bargauge", 0, 15, 12, 8, "percent", [2]string{`100 * strata_volume_used_bytes` + host + ` / strata_volume_size_bytes` + host, "{{volume}}"}),
		panel("Températures des disques", "timeseries", 12, 15, 12, 8, "celsius", [2]string{`strata_disk_temperature_celsius` + host, "{{device}} {{model}}"}),
		panel("Santé (RAID, ZFS, SMART)", "stat", 0, 23, 12, 6, "none", [2]string{`strata_raid_healthy` + host, "RAID {{array}}"}, [2]string{`strata_zpool_healthy` + host, "ZFS {{pool}}"}, [2]string{`strata_disk_smart_healthy` + host, "{{device}}"}),
		panel("Dernière sauvegarde réussie (il y a)", "stat", 12, 23, 12, 6, "s", [2]string{`time() - strata_backup_last_success_timestamp_seconds` + host, "{{task}}"}),
		panel("Onduleur", "timeseries", 0, 29, 12, 7, "percent", [2]string{`strata_ups_battery_charge_percent` + host, "batterie"}, [2]string{`strata_ups_load_percent` + host, "charge"}),
		panel("Sécurité", "stat", 12, 29, 12, 7, "none", [2]string{`strata_guard_blocked_addresses` + host, "adresses bloquées"}, [2]string{`strata_ransomware_blocked_accounts` + host, "comptes bloqués (rançongiciel)"}),
	}
	return map[string]any{
		"__inputs":      []any{map[string]any{"name": "DS_PROMETHEUS", "label": "Prometheus", "type": "datasource", "pluginId": "prometheus"}},
		"title":         "StrataOS",
		"uid":           "strataos",
		"schemaVersion": 39,
		"refresh":       "1m",
		"time":          map[string]string{"from": "now-24h", "to": "now"},
		"tags":          []string{"strataos", "nas"},
		"templating": map[string]any{"list": []any{map[string]any{
			"name": "instance", "label": "NAS", "type": "query", "multi": true, "includeAll": true,
			"datasource": map[string]string{"type": "prometheus", "uid": "${DS_PROMETHEUS}"},
			"query":      "label_values(strata_info, instance)", "refresh": 1,
		}}},
		"panels": panels,
	}
}
