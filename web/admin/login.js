const loginForm = document.getElementById('loginForm');
const adminKeyInput = document.getElementById('adminKey');
const loginError = document.getElementById('loginError');

function showLoginError(message) {
  loginError.hidden = false;
  loginError.textContent = message;
}

async function fetchJSON(path, options = {}) {
  const response = await fetch(path, {
    credentials: 'same-origin',
    ...options,
    headers: {
      'Content-Type': 'application/json',
      ...(options.headers || {}),
    },
  });

  const text = await response.text();
  let payload = {};
  if (text) {
    try {
      payload = JSON.parse(text);
    } catch {
      payload = { raw: text };
    }
  }

  if (!response.ok) {
    throw new Error(payload?.error?.message || payload?.message || payload?.raw || `HTTP ${response.status}`);
  }
  return payload;
}

async function bootstrap() {
  try {
    await fetchJSON('/v1/admin/session/me', { method: 'GET' });
    window.location.replace('/admin');
  } catch {
    adminKeyInput.focus();
  }
}

loginForm.addEventListener('submit', async (event) => {
  event.preventDefault();
  loginError.hidden = true;

  const adminKey = adminKeyInput.value.trim();
  if (!adminKey) {
    showLoginError('请先输入 admin key。');
    return;
  }

  try {
    await fetchJSON('/v1/admin/session/login', {
      method: 'POST',
      body: JSON.stringify({ admin_key: adminKey }),
    });
    window.location.replace('/admin');
  } catch (error) {
    showLoginError(error.message || '登录失败');
  }
});

bootstrap();
