package handler

import "net/http"

// Public resource for users without the installed app. The browser calls the
// same reauthentication and deletion service as mobile, without cookie auth.
func AccountDeletionPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; connect-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
	_, _ = w.Write([]byte(deletionPage))
}

const deletionPage = `<!doctype html><html lang="ru"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Avari Tabletop — удаление аккаунта</title>
<style>body{font:16px system-ui;max-width:36rem;margin:3rem auto;padding:1rem;background:#16181c;color:#eee}input,button{font:inherit;padding:.8rem;box-sizing:border-box;width:100%;margin:.5rem 0}label{display:block;margin-top:1rem}button{cursor:pointer}a{color:#cba979}#status{white-space:pre-wrap}</style>
<h1>Удаление аккаунта Avari Tabletop</h1><p>Коллекция, заметки, история чатов и данные аккаунта будут удалены без возможности восстановления. Доступ прекращается после принятия запроса. Завершение подтверждается только после очистки данных.</p>
<form id="form"><label>Email<input id="email" type="email" required autocomplete="username" maxlength="255"></label><label>Подтвердите пароль<input id="password" type="password" required autocomplete="current-password" maxlength="72"></label><button id="submit">Удалить аккаунт навсегда</button></form>
<p id="status" role="status" aria-live="polite"></p><button id="check" hidden>Проверить статус</button><noscript>Для отправки запроса включите JavaScript или воспользуйтесь удалением в профиле приложения.</noscript>
<script>
const form=document.getElementById('form'),statusEl=document.getElementById('status'),check=document.getElementById('check'),submit=document.getElementById('submit');
let receipt=null,access=null,key=null,timer=null,busy=false,credentials=null;
try{receipt=JSON.parse(sessionStorage.getItem('avari.deletion'));}catch(_){}
async function api(path,body,headers){const r=await fetch('/api/v1/'+path,{method:body?'POST':'GET',headers:Object.assign({'Content-Type':'application/json'},headers),body:body?JSON.stringify(body):undefined,credentials:'omit',cache:'no-store',redirect:'error'});if(!r.ok)throw new Error(r.status===401?'Проверьте пароль и войдите снова.':'Запрос не выполнен. Повторите позже.');return r.json();}
function accepted(){form.hidden=true;check.hidden=false;}
async function poll(){if(!receipt||busy)return;busy=true;check.disabled=true;clearTimeout(timer);try{const s=await api('deletions/'+encodeURIComponent(receipt.request_id),null,{'X-Deletion-Receipt':receipt.deletion_receipt});statusEl.textContent=s.status==='completed'?'Аккаунт и личные данные удалены.':s.status==='failed'?'Очистка временно не выполнена. Сервер повторит попытку.':'Запрос принят. Выполняется очистка данных.';if(s.status==='completed'){receipt=null;sessionStorage.removeItem('avari.deletion');check.hidden=true;}else timer=setTimeout(poll,15000);}catch(e){statusEl.textContent=e.message;}finally{busy=false;check.disabled=false;}}
form.onsubmit=async e=>{e.preventDefault();if(busy)return;if(!confirm('Удалить аккаунт и личные данные навсегда?'))return;busy=true;submit.disabled=true;try{const next={email:document.getElementById('email').value,password:document.getElementById('password').value};if(!access||!credentials||next.email!==credentials.email||next.password!==credentials.password){const s=await api('auth/login',next);access=s.access_token;key=crypto.randomUUID();credentials=next;}
receipt=await api('me/deletion',{password:next.password},{Authorization:'Bearer '+access,'Idempotency-Key':key});access=null;credentials=null;document.getElementById('password').value='';document.getElementById('email').value='';accepted();try{sessionStorage.setItem('avari.deletion',JSON.stringify({request_id:receipt.request_id,deletion_receipt:receipt.deletion_receipt}));}catch(_){}statusEl.textContent='Запрос принят. Выполняется очистка данных.';timer=setTimeout(poll,15000);}catch(e){statusEl.textContent=e.message;}finally{busy=false;submit.disabled=false;}};
check.onclick=poll;if(receipt&&receipt.request_id&&receipt.deletion_receipt){accepted();poll();}
</script></html>`
