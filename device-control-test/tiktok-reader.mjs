import { TikTokLiveConnection } from 'tiktok-live-connector';

const username = String(process.argv[2] || '').replace(/^@+/, '').trim();
const emit = (obj) => {
  try { process.stdout.write(JSON.stringify(obj) + '\n'); } catch {}
};
if (!/^[A-Za-z0-9._]{2,24}$/.test(username)) {
  emit({type:'status',status:'error',text:'INVALID_TIKTOK_HANDLE'});
  process.exit(2);
}

const connection = new TikTokLiveConnection(username, {
  enableExtendedGiftInfo: false,
  processInitialData: false,
});

connection.on('connected', () => emit({type:'status',status:'connected',text:'تم ربط TikTok محليًا'}));
connection.on('disconnected', () => emit({type:'status',status:'disconnected',text:'تم فصل TikTok'}));
connection.on('streamEnd', () => emit({type:'status',status:'ended',text:'انتهى بث TikTok'}));
connection.on('error', (err) => emit({type:'status',status:'warning',text:String(err?.message || err || 'TikTok error')}));
connection.on('chat', (data) => {
  const user = String(
    data?.user?.nickname ?? data?.nickname ?? data?.user?.uniqueId ?? data?.uniqueId ?? 'متابع'
  ).trim();
  const text = String(data?.comment ?? data?.message ?? data?.content ?? '').trim();
  const id = String(data?.msgId ?? data?.messageId ?? data?.id ?? `${Date.now()}-${Math.random()}`);
  if (text) emit({type:'chat',user,text,id});
});

const stop = async () => {
  try { connection.disconnect(); } catch {}
  setTimeout(() => process.exit(0), 50).unref?.();
};
process.on('SIGTERM', stop);
process.on('SIGINT', stop);

async function main() {
  try {
    await connection.connect();
  } catch (err) {
    emit({type:'status',status:'error',text:String(err?.message || err || 'تعذر ربط TikTok')});
    process.exit(1);
  }
  setInterval(() => {}, 1 << 30);
}

main().catch((err) => {
  emit({type:'status',status:'error',text:String(err?.message || err || 'تعذر تشغيل قارئ TikTok')});
  process.exit(1);
});
