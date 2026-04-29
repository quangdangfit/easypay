package handler

const (
	checkoutNotReadyHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta http-equiv="refresh" content="2">
<title>Preparing checkout…</title>
<style>
  body{font-family:-apple-system,BlinkMacSystemFont,Segoe UI,Roboto,sans-serif;background:#fafafa;display:flex;align-items:center;justify-content:center;height:100vh;margin:0;color:#1a1a1a}
  .card{background:#fff;border-radius:12px;padding:32px 40px;box-shadow:0 1px 3px rgba(0,0,0,0.08);text-align:center;max-width:420px}
  .spinner{width:36px;height:36px;border:3px solid #eee;border-top-color:#635bff;border-radius:50%;margin:0 auto 16px;animation:s 0.8s linear infinite}
  @keyframes s{to{transform:rotate(360deg)}}
  h1{font-size:18px;font-weight:600;margin:0 0 8px}
  p{font-size:14px;color:#666;margin:0}
</style>
</head>
<body>
  <div class="card">
    <div class="spinner"></div>
    <h1>Preparing your secure checkout…</h1>
    <p>This usually takes a second or two. The page will refresh automatically.</p>
  </div>
</body>
</html>`

	checkoutErrorHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>Checkout temporarily unavailable</title>
<style>
  body{font-family:-apple-system,BlinkMacSystemFont,Segoe UI,Roboto,sans-serif;background:#fafafa;display:flex;align-items:center;justify-content:center;height:100vh;margin:0;color:#1a1a1a}
  .card{background:#fff;border-radius:12px;padding:32px 40px;box-shadow:0 1px 3px rgba(0,0,0,0.08);text-align:center;max-width:480px}
  .icon{font-size:36px;margin-bottom:8px}
  h1{font-size:18px;font-weight:600;margin:0 0 8px}
  p{font-size:14px;color:#666;margin:0 0 16px;line-height:1.5}
  button{background:#635bff;color:#fff;border:0;border-radius:8px;padding:10px 20px;font-size:14px;font-weight:500;cursor:pointer}
  button:hover{background:#4f46e5}
  .ref{margin-top:16px;font-size:11px;color:#aaa;font-family:monospace}
</style>
</head>
<body>
  <div class="card">
    <div class="icon">⏳</div>
    <h1>Checkout temporarily unavailable</h1>
    <p>We couldn't reach our payment processor right now. Please try again in a moment, or contact the merchant if this persists.</p>
    <button onclick="location.reload()">Try again</button>
    <div class="ref">ref: %s</div>
  </div>
</body>
</html>`

	checkoutInvalidHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>Invalid checkout link</title>
<style>
  body{font-family:-apple-system,BlinkMacSystemFont,Segoe UI,Roboto,sans-serif;background:#fafafa;display:flex;align-items:center;justify-content:center;height:100vh;margin:0;color:#1a1a1a}
  .card{background:#fff;border-radius:12px;padding:32px 40px;box-shadow:0 1px 3px rgba(0,0,0,0.08);text-align:center;max-width:420px}
  .icon{font-size:36px;margin-bottom:8px}
  h1{font-size:18px;font-weight:600;margin:0 0 8px}
  p{font-size:14px;color:#666;margin:0;line-height:1.5}
</style>
</head>
<body>
  <div class="card">
    <div class="icon">🔗</div>
    <h1>This checkout link is no longer valid</h1>
    <p>It may have expired or been used. Please return to the merchant to start a new checkout.</p>
  </div>
</body>
</html>`
)
