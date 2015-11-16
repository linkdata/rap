/* ANSI-C code produced by gperf version 3.0.3 */
/* Command-line: /Applications/Xcode.app/Contents/Developer/Toolchains/XcodeDefault.xctoolchain/usr/bin/gperf rap_textmap.gperf  */
/* Computed positions: -k'1,$' */

#if !((' ' == 32) && ('!' == 33) && ('"' == 34) && ('#' == 35) \
      && ('%' == 37) && ('&' == 38) && ('\'' == 39) && ('(' == 40) \
      && (')' == 41) && ('*' == 42) && ('+' == 43) && (',' == 44) \
      && ('-' == 45) && ('.' == 46) && ('/' == 47) && ('0' == 48) \
      && ('1' == 49) && ('2' == 50) && ('3' == 51) && ('4' == 52) \
      && ('5' == 53) && ('6' == 54) && ('7' == 55) && ('8' == 56) \
      && ('9' == 57) && (':' == 58) && (';' == 59) && ('<' == 60) \
      && ('=' == 61) && ('>' == 62) && ('?' == 63) && ('A' == 65) \
      && ('B' == 66) && ('C' == 67) && ('D' == 68) && ('E' == 69) \
      && ('F' == 70) && ('G' == 71) && ('H' == 72) && ('I' == 73) \
      && ('J' == 74) && ('K' == 75) && ('L' == 76) && ('M' == 77) \
      && ('N' == 78) && ('O' == 79) && ('P' == 80) && ('Q' == 81) \
      && ('R' == 82) && ('S' == 83) && ('T' == 84) && ('U' == 85) \
      && ('V' == 86) && ('W' == 87) && ('X' == 88) && ('Y' == 89) \
      && ('Z' == 90) && ('[' == 91) && ('\\' == 92) && (']' == 93) \
      && ('^' == 94) && ('_' == 95) && ('a' == 97) && ('b' == 98) \
      && ('c' == 99) && ('d' == 100) && ('e' == 101) && ('f' == 102) \
      && ('g' == 103) && ('h' == 104) && ('i' == 105) && ('j' == 106) \
      && ('k' == 107) && ('l' == 108) && ('m' == 109) && ('n' == 110) \
      && ('o' == 111) && ('p' == 112) && ('q' == 113) && ('r' == 114) \
      && ('s' == 115) && ('t' == 116) && ('u' == 117) && ('v' == 118) \
      && ('w' == 119) && ('x' == 120) && ('y' == 121) && ('z' == 122) \
      && ('{' == 123) && ('|' == 124) && ('}' == 125) && ('~' == 126))
/* The character set is not based on ISO-646.  */
#error "gperf generated tables don't work with this execution character set. Please report a bug to <bug-gnu-gperf@gnu.org>."
#endif

#include <string.h>

#define TOTAL_KEYWORDS 82
#define MIN_WORD_LENGTH 2
#define MAX_WORD_LENGTH 27
#define MIN_HASH_VALUE 3
#define MAX_HASH_VALUE 109
/* maximum key range = 107, duplicates = 0 */

#ifdef __GNUC__
__inline
#else
#ifdef __cplusplus
inline
#endif
#endif
static unsigned int
hash (register const char *str, register unsigned int len)
{
  static const unsigned char asso_values[] =
    {
      110, 110, 110, 110, 110, 110, 110, 110, 110, 110,
      110, 110, 110, 110, 110, 110, 110, 110, 110, 110,
      110, 110, 110, 110, 110, 110, 110, 110, 110, 110,
      110, 110, 110, 110, 110, 110, 110, 110, 110, 110,
      110, 110, 110, 110, 110, 110, 110, 110, 110, 110,
      110, 110, 110,  60, 110, 110, 110, 110, 110, 110,
      110, 110, 110, 110, 110,   0, 110,   5,  65,  20,
        0,   0,  40,  15, 110, 110,  10,  30, 110,  35,
       40, 110,  35,  15,   0,  75,  50,  50,  20, 110,
      110, 110, 110, 110, 110, 110, 110,   5, 110,   0,
       25,   5, 110,  45,  10, 110, 110,  20,  60,   0,
        0,  10,  40, 110,  35,  40,   5,   5, 110,   0,
      110,  40, 110, 110, 110, 110, 110, 110, 110, 110,
      110, 110, 110, 110, 110, 110, 110, 110, 110, 110,
      110, 110, 110, 110, 110, 110, 110, 110, 110, 110,
      110, 110, 110, 110, 110, 110, 110, 110, 110, 110,
      110, 110, 110, 110, 110, 110, 110, 110, 110, 110,
      110, 110, 110, 110, 110, 110, 110, 110, 110, 110,
      110, 110, 110, 110, 110, 110, 110, 110, 110, 110,
      110, 110, 110, 110, 110, 110, 110, 110, 110, 110,
      110, 110, 110, 110, 110, 110, 110, 110, 110, 110,
      110, 110, 110, 110, 110, 110, 110, 110, 110, 110,
      110, 110, 110, 110, 110, 110, 110, 110, 110, 110,
      110, 110, 110, 110, 110, 110, 110, 110, 110, 110,
      110, 110, 110, 110, 110, 110, 110, 110, 110, 110,
      110, 110, 110, 110, 110, 110
    };
  return len + asso_values[(unsigned char)str[len - 1]] + asso_values[(unsigned char)str[0]];
}

static const unsigned char rap_textmap_lengths[] =
  {
     0,  0,  0,  3,  4,  5,  0,  2,  3,  0,  5,  6,  7, 13,
     9, 10,  6,  7,  8, 14, 15, 16, 12, 13, 19,  5, 16, 27,
     8, 14, 10,  6, 12,  8,  4, 10, 16, 17, 13, 19, 15,  6,
     7,  3,  4,  5, 16, 17, 13,  4, 10,  6,  7, 13,  4,  5,
     6,  7,  3, 19, 15,  6, 17, 18,  0,  0, 16,  7, 23,  4,
    15, 16, 12,  3,  4,  0, 11,  7, 13,  0, 25, 11, 12,  0,
     0,  5,  0,  7,  0,  4, 10,  6,  0,  0,  4, 15,  0,  0,
     0,  0,  0,  0,  0,  0,  0,  0,  0,  0,  0,  4
  };

static const char * const rap_textmap_words[] =
  {
    "", "", "",
    "GET",
    "From",
    "Allow",
    "",
    "Te",
    "Age",
    "",
    "close",
    "Accept",
    "CONNECT",
    "Authorization",
    "websocket",
    "Connection",
    "Cookie",
    "upgrade",
    "Location",
    "Accept-Charset",
    "Accept-Language",
    "Content-Location",
    "Content-Type",
    "Content-Range",
    "Content-Disposition",
    "TRACE",
    "Content-Language",
    "Access-Control-Allow-Origin",
    "If-Range",
    "Content-Length",
    "Set-Cookie",
    "Expect",
    "X-Csrf-Token",
    "If-Match",
    "Link",
    "keep-alive",
    "X-Xss-Protection",
    "If-Modified-Since",
    "If-None-Match",
    "If-Unmodified-Since",
    "X-Ua-Compatible",
    "Origin",
    "Trailer",
    "PUT",
    "POST",
    "Range",
    "X-Requested-With",
    "X-Forwarded-Proto",
    "Last-Modified",
    "Host",
    "Session-Id",
    "Pragma",
    "Refresh",
    "Accept-Ranges",
    "http",
    "https",
    "Server",
    "OPTIONS",
    "Via",
    "Proxy-Authorization",
    "Accept-Encoding",
    "Status",
    "Transfer-Encoding",
    "Proxy-Authenticate",
    "", "",
    "Content-Encoding",
    "Expires",
    "Content-Security-Policy",
    "Etag",
    "X-Forwarded-For",
    "Www-Authenticate",
    "X-Powered-By",
    "Dnt",
    "Date",
    "",
    "Content-Md5",
    "Referer",
    "Cache-Control",
    "",
    "Strict-Transport-Security",
    "Retry-After",
    "Max-Forwards",
    "", "",
    "PATCH",
    "",
    "Upgrade",
    "",
    "gzip",
    "User-Agent",
    "DELETE",
    "", "",
    "Vary",
    "Public-Key-Pins",
    "", "", "", "", "", "", "", "", "",
    "", "", "", "",
    "HEAD"
  };

const char *
rap_textmap_in_word_set (register const char *str, register unsigned int len)
{
  if (len <= MAX_WORD_LENGTH && len >= MIN_WORD_LENGTH)
    {
      unsigned int key = hash (str, len);

      if (key <= MAX_HASH_VALUE)
        if (len == rap_textmap_lengths[key])
          {
            register const char *s = rap_textmap_words[key];

            if (*str == *s && !memcmp (str + 1, s + 1, len - 1))
              return s;
          }
    }
  return 0;
}
#line 93 "rap_textmap.gperf"


unsigned int rap_textmap_max_key() {
  return MAX_HASH_VALUE + 2;
}

const char* rap_textmap_from_key(register unsigned int key, register size_t* p_len) {
  if (key < 2 || key > MAX_HASH_VALUE + 2) {
    *p_len = 0;
    return key == 1 ? "" : 0;
  }
  *p_len = rap_textmap_lengths[key - 2];
  return rap_textmap_words[key - 2];
}

unsigned int rap_textmap_to_key(register const char* str, register size_t len)
{
  if (!len)
    return 1;
  if (len <= MAX_WORD_LENGTH && len >= MIN_WORD_LENGTH) {
    unsigned int key = hash (str, (unsigned int)len);
    if (key <= MAX_HASH_VALUE)
      if (len == rap_textmap_lengths[key]) {
        register const char* s = rap_textmap_words[key];
        while (len--) {
          if (*str++ != *s++)
            return 0;
        }
        return 2 + key;
      }
  }
  return 0;
}
