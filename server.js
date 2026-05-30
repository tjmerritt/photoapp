/**
 * Mock API Server for PhotoApp
 * Run with: node server.js
 * Serves on http://localhost:3000
 */

const http = require('http');
const fs = require('fs');
const path = require('path');
const url = require('url');

const PORT = 3000;

const MOCK_USERS = {
  u1: { userid: 'u1', username: 'ansel_b', profile: { fullname: 'Ansel Blackwood', joined: '2021-03-15T09:00:00.000', link: '/profile/ansel_b', image: 'https://i.pravatar.cc/64?img=1' } },
  u2: { userid: 'u2', username: 'lena_ray', profile: { fullname: 'Lena Raynor',    joined: '2020-07-22T14:30:00.000', link: '/profile/lena_ray',  image: 'https://i.pravatar.cc/64?img=5' } },
  u3: { userid: 'u3', username: 'marco_v',  profile: { fullname: 'Marco Vidal',    joined: '2022-01-10T11:00:00.000', link: '/profile/marco_v',   image: 'https://i.pravatar.cc/64?img=8' } },
  u4: { userid: 'u4', username: 'suki_tm',  profile: { fullname: 'Suki Tamamoto',  joined: '2019-11-01T08:00:00.000', link: '/profile/suki_tm',   image: 'https://i.pravatar.cc/64?img=9' } },
  u5: { userid: 'u5', username: 'dev_null', profile: { fullname: 'Dev Null',       joined: '2023-05-05T00:00:00.000', link: '/profile/dev_null',  image: 'https://i.pravatar.cc/64?img=12' } },
};

const MOCK_PHOTOS = {
  'photo001': {
    photoid: 'photo001',
    image: {
      url: 'https://images.unsplash.com/photo-1506905925346-21bda4d32df4?w=1200&q=80',
      width: 1200,
      height: 800,
    },
    title: {
      text: 'Summit at Golden Hour',
      userid: 'u1',
      username: 'ansel_b',
      canedit: false,
    },
    description: 'Captured just as the last light kissed the ridgeline, this shot from the summit of Mt. Rainer looks east across the Cascade Range. The alpenglow turned the snowfields into rivers of rose gold — a moment that lasted only ninety seconds before the clouds rolled in and erased it completely.',
    labels: [
      { labelid: 'l1', name: 'Location',   value: 'Mt. Rainier, WA',  userid: 'u1', username: 'ansel_b' },
      { labelid: 'l2', name: 'Camera',     value: 'Sony A7R V',       userid: 'u1', username: 'ansel_b' },
      { labelid: 'l3', name: 'Lens',       value: '24-70mm f/2.8',    userid: 'u1', username: 'ansel_b' },
      { labelid: 'l4', name: 'ISO',        value: '400',              userid: 'u1', username: 'ansel_b' },
      { labelid: 'l5', name: 'Aperture',   value: 'f/8',              userid: 'u1', username: 'ansel_b' },
      { labelid: 'l6', name: 'Season',     value: 'Late Summer',      userid: 'u2', username: 'lena_ray' },
    ],
    emojis: [
      { emojiid: 'e1', emoji: '❤️', count: 142, users: [
        { id: 'u2', name: 'lena_ray', tn: 'https://i.pravatar.cc/32?img=5' },
        { id: 'u3', name: 'marco_v',  tn: 'https://i.pravatar.cc/32?img=8' },
        { id: 'u4', name: 'suki_tm',  tn: 'https://i.pravatar.cc/32?img=9' },
      ]},
      { emojiid: 'e2', emoji: '🔥', count: 87, users: [
        { id: 'u3', name: 'marco_v',  tn: 'https://i.pravatar.cc/32?img=8'  },
        { id: 'u5', name: 'dev_null', tn: 'https://i.pravatar.cc/32?img=12' },
      ]},
      { emojiid: 'e3', emoji: '😍', count: 64, users: [
        { id: 'u4', name: 'suki_tm', tn: 'https://i.pravatar.cc/32?img=9' },
      ]},
      { emojiid: 'e4', emoji: '🏔️', count: 33, users: [
        { id: 'u1', name: 'ansel_b', tn: 'https://i.pravatar.cc/32?img=1' },
      ]},
    ],
    related: [
      { photoid: 'photo002', imageurl: 'https://images.unsplash.com/photo-1464822759023-fed622ff2c3b?w=300&q=70', clickurl: '/photo?photoid=photo002', width: 300, height: 200 },
      { photoid: 'photo003', imageurl: 'https://images.unsplash.com/photo-1519681393784-d120267933ba?w=300&q=70', clickurl: '/photo?photoid=photo003', width: 300, height: 200 },
      { photoid: 'photo004', imageurl: 'https://images.unsplash.com/photo-1486870591958-9b9d0d1dda99?w=300&q=70', clickurl: '/photo?photoid=photo004', width: 300, height: 200 },
      { photoid: 'photo005', imageurl: 'https://images.unsplash.com/photo-1493246507139-91e8fad9978e?w=300&q=70', clickurl: '/photo?photoid=photo005', width: 300, height: 200 },
    ],
    comments: [
      {
        commentid: 'c1',
        author: { userid: 'u2', username: 'lena_ray', tn: 'https://i.pravatar.cc/48?img=5' },
        date: '2024-08-15T18:32:00.000',
        replycount: 3,
        comment: "This is absolutely stunning, Ansel! I was at Rainier last fall and the light was nothing like this — you must have been at exactly the right place at exactly the right moment. The way the alpenglow catches the ice formations in the foreground is just chef's kiss. How long did you hike to get to this vantage point?",
        repliesurl: '/api/v1/comments?photoid=photo001&parentid=c1',
      },
      {
        commentid: 'c2',
        author: { userid: 'u3', username: 'marco_v', tn: 'https://i.pravatar.cc/48?img=8' },
        date: '2024-08-15T20:14:00.000',
        replycount: 1,
        comment: "The color grading here is immaculate. I've been trying to nail this kind of warm-cool split for years. Did you do much post-processing or is this mostly SOOC?",
        repliesurl: '/api/v1/comments?photoid=photo001&parentid=c2',
      },
      {
        commentid: 'c3',
        author: { userid: 'u4', username: 'suki_tm', tn: 'https://i.pravatar.cc/48?img=9' },
        date: '2024-08-16T07:05:00.000',
        replycount: 0,
        comment: 'Added to my inspiration board immediately. Goals.',
        repliesurl: '/api/v1/comments?photoid=photo001&parentid=c3',
      },
      {
        commentid: 'c4',
        author: { userid: 'u5', username: 'dev_null', tn: 'https://i.pravatar.cc/48?img=12' },
        date: '2024-08-17T12:41:00.000',
        replycount: 0,
        comment: 'Technical question — what was your shutter speed here? The clouds have just enough motion blur to add drama but the foreground is tack sharp. Beautiful work.',
        repliesurl: '/api/v1/comments?photoid=photo001&parentid=c4',
      },
    ],
    commentsurl: null,
  },
};

const MOCK_REPLIES = {
  c1: [
    { commentid: 'r1-1', author: { userid: 'u1', username: 'ansel_b', tn: 'https://i.pravatar.cc/48?img=1' }, date: '2024-08-15T19:00:00.000', replycount: 0, comment: 'Thanks Lena! About a 6-hour round trip from the Paradise trailhead. Started at 4am to make sure I hit the summit before sunset.' },
    { commentid: 'r1-2', author: { userid: 'u2', username: 'lena_ray', tn: 'https://i.pravatar.cc/48?img=5' }, date: '2024-08-15T19:22:00.000', replycount: 0, comment: 'That dedication shows. Worth every step.' },
    { commentid: 'r1-3', author: { userid: 'u4', username: 'suki_tm',  tn: 'https://i.pravatar.cc/48?img=9' }, date: '2024-08-16T08:00:00.000', replycount: 0, comment: 'Putting this hike on my bucket list right now.' },
  ],
  c2: [
    { commentid: 'r2-1', author: { userid: 'u1', username: 'ansel_b', tn: 'https://i.pravatar.cc/48?img=1' }, date: '2024-08-15T21:00:00.000', replycount: 0, comment: 'Light editing — pulled shadows, slight warmth boost in Lightroom, and a luminosity mask on the sky. The scene really did look like that.' },
  ],
};

function parseQuery(queryStr) {
  const params = {};
  if (!queryStr) return params;
  queryStr.split('&').forEach(pair => {
    const [k, v] = pair.split('=');
    params[decodeURIComponent(k)] = decodeURIComponent(v || '');
  });
  return params;
}

function json(res, data, status = 200) {
  res.writeHead(status, {
    'Content-Type': 'application/json',
    'Access-Control-Allow-Origin': '*',
    'Access-Control-Allow-Methods': 'GET, OPTIONS',
    'Access-Control-Allow-Headers': 'Content-Type',
  });
  res.end(JSON.stringify(data, null, 2));
}

function notFound(res, msg) { json(res, { error: msg || 'Not found' }, 404); }

function serveFile(res, filePath, contentType) {
  fs.readFile(filePath, (err, data) => {
    if (err) { notFound(res, 'File not found'); return; }
    res.writeHead(200, { 'Content-Type': contentType });
    res.end(data);
  });
}

const server = http.createServer((req, res) => {
  const parsed = url.parse(req.url);
  const pathname = parsed.pathname;
  const query = parseQuery(parsed.query);

  if (req.method === 'OPTIONS') {
    res.writeHead(204, { 'Access-Control-Allow-Origin': '*', 'Access-Control-Allow-Methods': 'GET, OPTIONS' });
    res.end();
    return;
  }

  if (pathname === '/' || pathname === '/index.html') {
    serveFile(res, path.join(__dirname, 'index.html'), 'text/html');
    return;
  }

  if (pathname === '/api/v1/photo') {
    const photo = MOCK_PHOTOS[query.photoid];
    if (!photo) { notFound(res, `Photo '${query.photoid}' not found`); return; }
    json(res, photo);
    return;
  }

  if (pathname === '/api/v1/labels') {
    const photo = MOCK_PHOTOS[query.photoid];
    if (!photo) { notFound(res, 'Photo not found'); return; }
    const offset = parseInt(query.offset) || 0;
    const limit  = parseInt(query.limit)  || 10;
    const all = photo.labels;
    const slice = all.slice(offset, offset + limit);
    const base = `/api/v1/labels?photoid=${query.photoid}&limit=${limit}`;
    json(res, { photoid: query.photoid, offset, pages: {
      count: Math.ceil(all.length / limit), current: Math.floor(offset / limit) + 1,
      first: `${base}&offset=0`, last: `${base}&offset=${Math.max(0, Math.floor((all.length-1)/limit)*limit)}`,
      next: offset+limit < all.length ? `${base}&offset=${offset+limit}` : null,
      prev: offset > 0 ? `${base}&offset=${Math.max(0,offset-limit)}` : null,
    }, labels: slice });
    return;
  }

  if (pathname === '/api/v1/emojis') {
    const photo = MOCK_PHOTOS[query.photoid];
    if (!photo) { notFound(res, 'Photo not found'); return; }
    const offset = parseInt(query.offset) || 0;
    const limit  = parseInt(query.limit)  || 10;
    const all = photo.emojis;
    const slice = all.slice(offset, offset + limit);
    const base = `/api/v1/emojis?photoid=${query.photoid}&limit=${limit}`;
    json(res, { photoid: query.photoid, offset, pages: {
      count: Math.ceil(all.length / limit), current: Math.floor(offset / limit) + 1,
      first: `${base}&offset=0`, last: `${base}&offset=${Math.max(0, Math.floor((all.length-1)/limit)*limit)}`,
      next: offset+limit < all.length ? `${base}&offset=${offset+limit}` : null,
      prev: offset > 0 ? `${base}&offset=${Math.max(0,offset-limit)}` : null,
    }, emojis: slice });
    return;
  }

  if (pathname === '/api/v1/emoji/users') {
    let allUsers = [];
    Object.values(MOCK_PHOTOS).forEach(p => p.emojis.forEach(e => { if (e.emojiid === query.emoji) allUsers = e.users; }));
    const offset = parseInt(query.offset) || 0;
    const limit  = parseInt(query.limit)  || 10;
    const slice = allUsers.slice(offset, offset + limit);
    const base = `/api/v1/emoji/users?emoji=${query.emoji}&limit=${limit}`;
    json(res, { emojiid: query.emoji, offset, pages: {
      count: Math.ceil(allUsers.length / limit), current: Math.floor(offset / limit) + 1,
      first: `${base}&offset=0`, last: `${base}&offset=${Math.max(0,Math.floor((allUsers.length-1)/limit)*limit)}`,
      next: offset+limit < allUsers.length ? `${base}&offset=${offset+limit}` : null,
      prev: offset > 0 ? `${base}&offset=${Math.max(0,offset-limit)}` : null,
    }, users: slice });
    return;
  }

  if (pathname === '/api/v1/comments') {
    const offset = parseInt(query.offset) || 0;
    const limit  = parseInt(query.limit)  || 10;
    let all = [];
    if (query.parentid) {
      all = MOCK_REPLIES[query.parentid] || [];
    } else {
      const photo = MOCK_PHOTOS[query.photoid];
      if (!photo) { notFound(res, 'Photo not found'); return; }
      all = photo.comments;
    }
    const slice = all.slice(offset, offset + limit);
    const baseQ = query.parentid
      ? `/api/v1/comments?photoid=${query.photoid}&parentid=${query.parentid}&limit=${limit}`
      : `/api/v1/comments?photoid=${query.photoid}&limit=${limit}`;
    json(res, { photoid: query.photoid, parentid: query.parentid || null, offset, pages: {
      count: Math.ceil(all.length / limit), current: Math.floor(offset / limit) + 1,
      first: `${baseQ}&offset=0`, last: `${baseQ}&offset=${Math.max(0,Math.floor((all.length-1)/limit)*limit)}`,
      next: offset+limit < all.length ? `${baseQ}&offset=${offset+limit}` : null,
      prev: offset > 0 ? `${baseQ}&offset=${Math.max(0,offset-limit)}` : null,
    }, comments: slice });
    return;
  }

  if (pathname === '/api/v1/user') {
    const user = MOCK_USERS[query.userid];
    if (!user) { notFound(res, `User '${query.userid}' not found`); return; }
    json(res, user);
    return;
  }

  notFound(res, 'Unknown endpoint');
});

server.listen(PORT, () => {
  console.log(`\n📷  PhotoApp Mock API Server running at http://localhost:${PORT}`);
  console.log(`   Open http://localhost:${PORT}/?photoid=photo001 to view the app\n`);
  console.log('Available API endpoints:');
  console.log('  GET /api/v1/photo?photoid=photo001');
  console.log('  GET /api/v1/labels?photoid=photo001&offset=0&limit=10');
  console.log('  GET /api/v1/emojis?photoid=photo001&offset=0&limit=10');
  console.log('  GET /api/v1/emoji/users?emoji=e1&offset=0&limit=10');
  console.log('  GET /api/v1/comments?photoid=photo001&offset=0&limit=10');
  console.log('  GET /api/v1/comments?photoid=photo001&parentid=c1&offset=0&limit=10');
  console.log('  GET /api/v1/user?userid=u1\n');
});
