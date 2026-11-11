# shellcheck shell=bash
# Search inside documents (0.12): the docsearch component (pdftotext,
# Tesseract), indexing of a share followed through its change journal,
# snippets, filters, OCR, rights at query time (a share the user cannot
# read), incremental updates and a rebuild. Sample documents are generated
# by vm/testdata/docsearch/make-samples.py (committed in samples/, CC0).
section "Document search"
DD=/srv/strata/main/Docs
DV=/srv/strata/main/DocVault
c POST /api/shares -d "{\"name\":\"Docs\",\"path\":\"$DD\",\"smb\":true,\"permissions\":{\"@famille\":\"ro\"}}" >/dev/null
c POST /api/shares -d "{\"name\":\"DocVault\",\"path\":\"$DV\",\"smb\":true,\"permissions\":{}}" >/dev/null
for f in vm/testdata/docsearch/samples/*; do curl -s -b "$J" -H 'X-Strata: 1' -F "file=@$f;filename=team/$(basename "$f")" "$B/api/files/upload?path=$DD" >/dev/null; done
curl -s -b "$J" -H 'X-Strata: 1' -F "file=@vm/testdata/docsearch/samples/report.docx;filename=secret-report.docx" "$B/api/files/upload?path=$DV" >/dev/null
vm "sudo mkdir -p $DD/.git && echo 'lighthouse hidden' | sudo tee $DD/.git/notes.txt >/dev/null"

check "search off before folders are chosen" "$(c GET '/api/search/docs?q=lighthouse' | py 'print(d["enabled"])')" "False"
check "tools installed" "$(job "$(c POST /api/system/components/docsearch/install)")" "success"
check "folder outside the storage refused" "$(c PUT /api/search/docs/settings -d '{"folders":["/etc"]}')" "inside the NAS storage"
check "folders chosen, OCR on" "$(c PUT /api/search/docs/settings -d "{\"folders\":[\"$DD\",\"$DV\"],\"ocr\":true,\"limits\":{\"anyLoad\":true}}")" "ok"
for _ in $(seq 1 60); do
  ST=$(c GET /api/search/docs/status)
  [ "$(echo "$ST" | py 'print(d.get("stats",{}).get("docs",0) >= 11 and not d["progress"]["running"])')" = True ] && break
  sleep 5
done
check "documents indexed" "$(echo "$ST" | py 'print(d["stats"]["docs"], d["stats"]["failed"], d["stats"]["bytes"] > 0)')" "11 0 True"
s() { c GET "/api/search/docs?$1" | py 'print(" ".join(h["name"] for h in d["hits"]))'; }
check "Word" "$(s 'q=lighthouse%20renovation')" "report.docx"
check "administrator finds every folder" "$(s 'q=lighthouse')" "secret-report.docx"
check "no .git" "$(c GET '/api/search/docs?q=lighthouse' | py 'print(any(".git" in h["path"] for h in d["hits"]))')" "False"
check "PDF" "$(s 'q=marmalade')" "invoice-2024.pdf"
check "spreadsheet" "$(s 'q=pineapple')" "budget.xlsx"
check "presentation" "$(s 'q=telescope')" "slides.pptx"
check "OpenDocument, accents ignored" "$(s 'q=myrtille')" "notes.odt"
check "e-mail" "$(s 'q=windmill%20hazelnut')" "meeting.eml"
check "Markdown and code" "$(s 'q=tomatoes') $(s 'q=zeppelin_launch')" "README.md server.py"
check "OCR of a picture" "$(s 'q=kangaroo')" "scan.png"
check "snippet highlighted" "$(c GET '/api/search/docs?q=marmalade' | py 'print([p for p in d["hits"][0]["snippet"] if p.get("h")])')" "'Marmalade'"
check "phrase" "$(s 'q=%22due%20total%22') | $(s 'q=%22total%20due%22')" " | invoice-2024.pdf"
check "type filter" "$(s 'q=lighthouse&type=pdf')" ""
check "folder filter" "$(s "q=lighthouse&folder=$DV")" "secret-report.docx"
check "date filter" "$(c GET '/api/search/docs?q=lighthouse&from=2099-01-01' | py 'print(d["total"])')" "0"
check "size filter" "$(c GET '/api/search/docs?q=lighthouse&minSize=100000000' | py 'print(d["total"])')" "0"

# Rights at query time: alice reads Docs, not DocVault.
check "user gets hits of readable folders" "$(u GET '/api/search/docs?q=lighthouse' | py 'print(sorted(h["name"] for h in d["hits"]))')" "['report.docx']"
check "user's folder filter cannot reach the vault" "$(u GET "/api/search/docs?q=lighthouse&folder=$DV" | py 'print(d["total"])')" "0"
check "user does not see the settings" "$(u GET /api/search/docs/status | py 'print("folders" in d, d["enabled"])')" "False True"
check "settings for administrators only" "$(u PUT /api/search/docs/settings -d '{"folders":[]}')" "administrator"

# Incremental: a file written over the system (not the API) appears, then goes.
vm "echo 'The quokka smiles at the camera' | sudo tee $DD/team/quokka.txt >/dev/null"
for _ in $(seq 1 30); do [ "$(s 'q=quokka')" = quokka.txt ] && break; sleep 3; done
check "new file indexed within seconds" "$(s 'q=quokka')" "quokka.txt"
vm "sudo rm $DD/team/quokka.txt"
for _ in $(seq 1 30); do [ -z "$(s 'q=quokka')" ] && break; sleep 3; done
check "deleted file gone from the index" "[$(s 'q=quokka')]" "[]"

check "rebuild" "$(c POST /api/search/docs/rebuild)" "ok"
for _ in $(seq 1 60); do [ "$(s 'q=pineapple')" = budget.xlsx ] && break; sleep 3; done
check "index rebuilt" "$(s 'q=pineapple')" "budget.xlsx"

# Clean up.
c PUT /api/search/docs/settings -d '{"folders":[]}' >/dev/null
for sh in Docs DocVault; do c DELETE "/api/shares/$sh" >/dev/null; done
vm "sudo rm -rf $DD $DV"
