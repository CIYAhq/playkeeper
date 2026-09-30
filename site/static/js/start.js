// /start, where the Meta ads land (site.js plays its film). Whop's ad pixel,
// on this page alone (Settings.WhopPixel, in data-whop-pixel), counts the
// visit, a copy of the install command, the page sent to a computer and the
// live demo opened, from site.js's playkeeper:count events; it doesn't load
// when the browser sends Global Privacy Control or Do Not Track.
(function () {
  var holder = document.querySelector('[data-whop-pixel]');
  var account = holder && holder.getAttribute('data-whop-pixel');
  if (!account || navigator.globalPrivacyControl === true || navigator.doNotTrack === '1' || window.doNotTrack === '1') return;

  // Whop's snippet, verbatim: the Content-Security-Policy allows no inline
  // script, and only this page's allows https://t.whop.tw.
  !function(w,d,s,u,n,a,b){if(w[n])return;a=w[n]={q:[],t:+new Date,s:[],o:u,track:function(){a.q.push([+new Date].concat([].slice.call(arguments)))},setScope:function(){a.s=[].slice.call(arguments).filter(function(x){return typeof x==="string"});a.q.push([+new Date,"setScope"].concat(a.s))},scope:function(){var c=[].slice.call(arguments);return{track:function(){a.q.push([+new Date].concat([].slice.call(arguments)).concat([{__scope:c}]))}}}};b=d.createElement(s);b.async=1;b.src=u+"/s.js";d.getElementsByTagName(s)[0].parentNode.insertBefore(b,d.getElementsByTagName(s)[0])}(window,document,"script","https://t.whop.tw","whop");
  window.whop.setScope(account);
  window.whop.track('page');

  // One id per page view, for every event: Whop counts an event name and id
  // once, so copying twice in one visit is one conversion.
  var eventId = window.crypto && crypto.randomUUID ? crypto.randomUUID() : Date.now() + '-' + Math.random().toString(36).slice(2);
  var forwarded = ['install_copied', 'install_shared', 'demo_opened'];
  document.addEventListener('playkeeper:count', function (e) {
    if (forwarded.indexOf(e.detail.name) !== -1) window.whop.track(e.detail.name, { event_id: eventId });
  });
})();
