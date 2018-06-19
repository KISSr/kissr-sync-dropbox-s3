require('dotenv').config();
require('isomorphic-fetch');
const AWS = require('aws-sdk');
const { Pool } = require('pg');
const pool = new Pool();
const s3 = new AWS.S3();
const Dropbox = require('dropbox').Dropbox;
var url = require('url');

const bodyParser = require('body-parser');
const express = require('express');
const app = express();
app.use(bodyParser.json());
var cursors = {};

app.get('/', (req, res) => res.send(req.query.challenge))
app.post('/', async (req, res) => {
  req.body.list_folder.accounts.forEach(async (userId) => {
    let dbx = new Dropbox({ accessToken: await getToken(userId) });
    syncFolder(userId, dbx);
  })
});

async function getToken(userId) {
  return (await pool.query('SELECT token FROM users WHERE dropbox_user_id=$1', [userId])).rows[0].token
}

async function syncFolder(userId, dbx, lastCursor) {
  const {
    entries,
    cursor,
    has_more,
  } = await (lastCursor ? dbx.filesListFolderContinue({cursor: lastCursor}):
    dbx.filesListFolder({
      path: '',
      recursive: true,
    }));

    cursors[userId] = cursor;
    entries.forEach((entry) => {
      if(entry[".tag"] == "file" && shouldSync(userId, entry.path_display)) {
        copyToS3(dbx, entry.path_display)
      }
    });
    if(has_more) {
      syncFolder(userId, dbx, cursor);
    }
}

async function shouldSync(userId, path) {
  let domain = path.split("/")[1];
  return (await pool.query('SELECT 1 FROM users JOIN sites ON users.id=sites.user_id WHERE dropbox_user_id=$1 and domain=$2', [userId, domain]).rowCount > 0)
}

async function copyToS3(dbx, path) {
  try {
    download = await dbx.filesDownload({path})
    s3.putObject({
      Bucket: 'kissr',
      Key: path.slice(1),
      Body: download.fileBinary,
      ACL: 'public-read',
    }, function() {
      console.log(`Copied: ${path.slice(1)}`)
    })
  } catch (e) {
    console.log(`Oops: ${path}`);
  }
}


app.listen(8080);
