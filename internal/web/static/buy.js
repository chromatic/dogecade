(function () {
  var statusEl = document.getElementById('status');
  if (!statusEl) return;
  var address = statusEl.getAttribute('data-address');
  // 'tokens' (default): the /buy page crediting the signed-in customer's
  // balance. 'direct': the /m/{slug} direct-pay-to-machine section, where
  // there's no account or balance to speak of.
  var mode = statusEl.getAttribute('data-mode') || 'tokens';
  var creditedMessage = mode === 'direct'
    ? 'Payment confirmed! Your credit is on its way to the machine.'
    : 'Payment confirmed! Tokens added to your balance.';

  function render(data) {
    if (data.state === 'credited') {
      statusEl.textContent = creditedMessage;
    } else if (data.state === 'confirmed' || data.state === 'seen') {
      statusEl.textContent = 'Payment seen (' + data.confirmations + '/' + data.min_confirmations + ' confirmations)...';
    } else {
      statusEl.textContent = 'Waiting for payment...';
    }
  }

  function pollOnce() {
    fetch('/buy/status?address=' + encodeURIComponent(address))
      .then(function (r) { return r.json(); })
      .then(render)
      .catch(function () {});
  }

  if (window.EventSource) {
    var es = new EventSource('/buy/events?address=' + encodeURIComponent(address));
    es.onmessage = function (ev) {
      try { render(JSON.parse(ev.data)); } catch (e) {}
    };
    es.onerror = function () {
      es.close();
      setInterval(pollOnce, 3000);
      pollOnce();
    };
  } else {
    setInterval(pollOnce, 3000);
    pollOnce();
  }
})();
